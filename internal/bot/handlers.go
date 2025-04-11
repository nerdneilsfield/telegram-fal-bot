package bot

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	// Use context for potentially long running operations

	// Optional balance storage
	"github.com/nerdneilsfield/telegram-fal-bot/pkg/falapi"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

func HandleUpdate(update tgbotapi.Update, deps BotDeps) {
	if update.Message != nil { // 处理普通消息或命令
		HandleMessage(update.Message, deps)
	} else if update.CallbackQuery != nil { // 处理内联键盘回调
		HandleCallbackQuery(update.CallbackQuery, deps)
	}
}

func HandleMessage(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID

	// 清理可能过期的状态
	deps.StateManager.ClearState(userID)

	// 命令处理
	if message.IsCommand() {
		switch message.Command() {
		case "start":
			reply := tgbotapi.NewMessage(chatID, "欢迎使用 Flux LoRA 图片生成 Bot！\n发送图片进行描述和生成，或直接发送描述文本生成图片。\n使用 /balance 查看余额。\n使用 /loras 查看可用风格。\n使用 /version 查看版本信息。")
			reply.ParseMode = tgbotapi.ModeMarkdown
			deps.Bot.Send(reply)
		case "balance":
			if deps.BalanceManager != nil {
				balance := deps.BalanceManager.GetBalance(userID)
				reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("您当前的余额是: %.2f 点", balance))
				deps.Bot.Send(reply)
			} else {
				deps.Bot.Send(tgbotapi.NewMessage(chatID, "未启用余额功能。"))
			}

			if deps.Authorizer.IsAdmin(userID) {
				go func() {
					reply := tgbotapi.NewMessage(chatID, "你是管理员，正在获取实际余额...")
					msg, err := deps.Bot.Send(reply)
					if err != nil {
						deps.Logger.Error("Failed to send admin balance message", zap.Error(err), zap.Int64("user_id", userID))
						return
					}
					balance, err := deps.FalClient.GetAccountBalance()
					if err != nil {
						deps.Logger.Error("Failed to get account balance", zap.Error(err), zap.Int64("user_id", userID))
						reply := tgbotapi.NewEditMessageText(chatID, msg.MessageID, fmt.Sprintf("获取余额失败。%s", err.Error()))
						deps.Bot.Send(reply)
					} else {
						reply := tgbotapi.NewEditMessageText(chatID, msg.MessageID, fmt.Sprintf("您实际的账户余额是: %.2f USD", balance))
						deps.Bot.Send(reply)
					}
				}()
			}
		case "loras":
			var loraList strings.Builder
			loraList.WriteString("可用的 LoRA 风格:\n")
			for _, lora := range deps.LoRA {
				loraList.WriteString(fmt.Sprintf("- %s\n", lora.Name))
			}
			// if userID in config.Admins.AdminUserIDs
			if deps.Authorizer.IsAdmin(userID) {
				loraList.WriteString("Base LoRA 风格:\n")
				for _, lora := range deps.BaseLoRA {
					loraList.WriteString(fmt.Sprintf("- %s\n", lora.Name))
				}
			}
			reply := tgbotapi.NewMessage(chatID, loraList.String())
			reply.ParseMode = tgbotapi.ModeMarkdown
			deps.Bot.Send(reply)

		case "version":
			reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("当前版本: %s\n构建日期: %s\nGo 版本: %s", deps.Version, deps.BuildDate, runtime.Version()))
			reply.ParseMode = tgbotapi.ModeMarkdown
			deps.Bot.Send(reply)

		// 可以添加其他命令，如 /help
		default:
			deps.Bot.Send(tgbotapi.NewMessage(chatID, "未知命令。"))
		}
		return // 命令处理完毕
	}

	// 图片消息处理
	if message.Photo != nil && len(message.Photo) > 0 {
		HandlePhotoMessage(message, deps)
		return
	}

	// 文本消息处理 (假设是直接提供 prompt)
	if message.Text != "" {
		HandleTextMessage(message, deps)
		return
	}

	// 其他类型消息忽略
	deps.Logger.Debug("Ignoring non-command, non-photo, non-text message", zap.Int64("user_id", userID))
}

func HandlePhotoMessage(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID

	// 1. Get image URL from Telegram
	if len(message.Photo) == 0 {
		deps.Logger.Warn("Photo message received but no photo data", zap.Int64("user_id", userID))
		deps.Bot.Send(tgbotapi.NewMessage(chatID, "Error: No photo data found in the message."))
		return
	}
	photo := message.Photo[len(message.Photo)-1] // Highest resolution
	fileConfig := tgbotapi.FileConfig{FileID: photo.FileID}
	file, err := deps.Bot.GetFile(fileConfig)
	if err != nil {
		deps.Logger.Error("Failed to get file info from Telegram", zap.Error(err), zap.Int64("user_id", userID))
		deps.Bot.Send(tgbotapi.NewMessage(chatID, "Error processing image file. Please try again."))
		return
	}
	imageURL := file.Link(deps.Bot.Token)

	// 2. Send initial "Submitting..." message
	waitMsg := tgbotapi.NewMessage(chatID, "⏳ Submitting image for captioning...")
	sentMsg, err := deps.Bot.Send(waitMsg)
	if err != nil {
		deps.Logger.Error("Failed to send initial wait message", zap.Error(err), zap.Int64("user_id", userID))
		// Continue anyway, user might still get the result
	}
	initialMessageID := 0
	if sentMsg.MessageID != 0 {
		initialMessageID = sentMsg.MessageID
	}

	// 3. Start captioning process in a Goroutine
	go func(imgURL string, originalChatID int64, originalUserID int64, msgIDToEdit int) {
		captionEndpoint := deps.Config.APIEndpoints.FlorenceCaption // Get caption endpoint from config
		pollInterval := 5 * time.Second                             // Adjust interval as needed
		captionTimeout := 2 * time.Minute                           // Timeout for captioning

		// 3a. Submit caption request
		requestID, err := deps.FalClient.SubmitCaptionRequest(imgURL)
		if err != nil {
			deps.Logger.Error("Failed to submit caption request", zap.Error(err), zap.Int64("user_id", originalUserID))
			errorText := fmt.Sprintf("❌ Error submitting caption request: %s", err.Error())
			if msgIDToEdit != 0 {
				deps.Bot.Send(tgbotapi.NewEditMessageText(originalChatID, msgIDToEdit, errorText))
			} else {
				deps.Bot.Send(tgbotapi.NewMessage(originalChatID, errorText))
			}
			return
		}

		deps.Logger.Info("Submitted caption task", zap.Int64("user_id", originalUserID), zap.String("request_id", requestID))
		statusUpdate := fmt.Sprintf("⏳ Caption task submitted (ID: ...%s). Waiting for result...", truncateID(requestID))
		if msgIDToEdit != 0 {
			deps.Bot.Send(tgbotapi.NewEditMessageText(originalChatID, msgIDToEdit, statusUpdate))
		} else {
			// If initial message failed, send a new one
			// Potential race condition if initial message sends *after* this check
			deps.Bot.Send(tgbotapi.NewMessage(originalChatID, statusUpdate))
		}

		// 3b. Poll for caption result
		ctx, cancel := context.WithTimeout(context.Background(), captionTimeout)
		defer cancel()
		captionText, err := deps.FalClient.PollForCaptionResult(ctx, requestID, captionEndpoint, pollInterval)

		if err != nil {
			deps.Logger.Error("Polling/captioning failed", zap.Error(err), zap.Int64("user_id", originalUserID), zap.String("request_id", requestID))
			errorText := fmt.Sprintf("❌ Failed to get caption: %s", err.Error())
			if msgIDToEdit != 0 {
				deps.Bot.Send(tgbotapi.NewEditMessageText(originalChatID, msgIDToEdit, errorText))
			} else {
				deps.Bot.Send(tgbotapi.NewMessage(originalChatID, errorText))
			}
			return
		}

		deps.Logger.Info("Caption received successfully", zap.Int64("user_id", originalUserID), zap.String("request_id", requestID), zap.String("caption", captionText))

		// 4. Caption Success: Store state and ask for confirmation
		newState := &UserState{
			UserID:          originalUserID,
			Action:          "awaiting_caption_confirmation", // New state: wait for user confirmation
			OriginalCaption: captionText,                     // Store the received caption
			SelectedLoras:   []string{},                      // Initialize empty slice
		}
		deps.StateManager.SetState(originalUserID, newState)

		// 5. Send caption and confirmation keyboard
		msgText := fmt.Sprintf("✅ Caption received:\n```\n%s\n```\nConfirm generation with this caption, or cancel?", captionText)

		confirmationKeyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ Confirm Generation", "caption_confirm"),
				tgbotapi.NewInlineKeyboardButtonData("❌ Cancel", "caption_cancel"),
			),
		)

		var finalMsg tgbotapi.Chattable
		if msgIDToEdit != 0 {
			editMsg := tgbotapi.NewEditMessageText(originalChatID, msgIDToEdit, msgText)
			editMsg.ParseMode = tgbotapi.ModeMarkdown
			editMsg.ReplyMarkup = &confirmationKeyboard // Set the confirmation keyboard
			finalMsg = editMsg
		} else {
			// Send as new message if editing failed initially
			newMsg := tgbotapi.NewMessage(originalChatID, msgText)
			newMsg.ParseMode = tgbotapi.ModeMarkdown
			newMsg.ReplyMarkup = &confirmationKeyboard // Set the confirmation keyboard
			finalMsg = newMsg
		}
		_, err = deps.Bot.Send(finalMsg)
		if err != nil {
			deps.Logger.Error("Failed to send caption result & confirmation keyboard", zap.Error(err), zap.Int64("user_id", originalUserID))
		}

	}(imageURL, chatID, userID, initialMessageID) // Pass necessary variables to goroutine

	// Return immediately, the goroutine handles the rest
}

func HandleTextMessage(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID
	prompt := message.Text

	// 1. 检查是否是回复（可能用于编辑 caption）- 暂不实现编辑逻辑简化流程

	// 2. 直接进入 LoRA 选择流程
	newState := &UserState{
		UserID:          userID,
		Action:          "awaiting_lora_selection",
		OriginalCaption: prompt, // 使用文本消息作为 Prompt
		SelectedLoras:   []string{},
	}
	deps.StateManager.SetState(userID, newState)

	msgText := fmt.Sprintf("使用描述：\n```\n%s\n```\n请选择要使用的 LoRA 风格进行生成:", prompt)
	reply := tgbotapi.NewMessage(chatID, msgText)
	reply.ParseMode = tgbotapi.ModeMarkdown
	keyboard := CreateLoraSelectionKeyboard(deps.LoRA)
	reply.ReplyMarkup = keyboard
	_, err := deps.Bot.Send(reply)
	if err != nil {
		deps.Logger.Error("Failed to send prompt & lora selection", zap.Error(err), zap.Int64("user_id", userID))
	}
}

func HandleCallbackQuery(callbackQuery *tgbotapi.CallbackQuery, deps BotDeps) {
	userID := callbackQuery.From.ID
	chatID := callbackQuery.Message.Chat.ID
	messageID := callbackQuery.Message.MessageID
	data := callbackQuery.Data // e.g., "select_lora:lora-id-style-a", "lora_done"

	// 回复 Callback Query，消除按钮上的加载状态
	callbackResp := tgbotapi.NewCallback(callbackQuery.ID, "") // 可以显示短暂提示

	currentState, ok := deps.StateManager.GetState(userID)
	if !ok {
		// logger.Warn("Received callback for user with no state", zap.Int64("user_id", userID), zap.String("data", data))
		callbackResp.Text = "操作已过期或无效，请重新开始。"
		// deps.Bot.AnswerCallbackQuery(callbackResp)
		if _, err := deps.Bot.Request(callbackResp); err != nil {
			deps.Logger.Error("Failed to answer callback query (no state)", zap.Error(err), zap.Int64("user_id", userID))
		}
		// 可以尝试编辑原消息提示用户
		deps.Bot.Send(tgbotapi.NewEditMessageText(chatID, messageID, "操作已过期或无效，请重新发送图片或描述。"))
		return
	}

	// 处理 LoRA 选择
	if strings.HasPrefix(data, "select_lora:") {
		if currentState.Action != "awaiting_lora_selection" {
			callbackResp.Text = "当前无法选择 LoRA。"
			// deps.Bot.AnswerCallbackQuery(callbackResp)
			if _, err := deps.Bot.Request(callbackResp); err != nil {
				deps.Logger.Error("Failed to answer callback query (cannot select lora now)", zap.Error(err), zap.Int64("user_id", userID))
			}
			return
		}
		loraID := strings.TrimPrefix(data, "select_lora:")

		// 更新状态中的选择
		_, updated := deps.StateManager.ToggleLoraSelection(userID, loraID)
		if !updated {
			// 状态不匹配或错误
			callbackResp.Text = "选择 LoRA 出错。"
			// deps.Bot.AnswerCallbackQuery(callbackResp)
			if _, err := deps.Bot.Request(callbackResp); err != nil {
				deps.Logger.Error("Failed to answer callback query (toggle lora error)", zap.Error(err), zap.Int64("user_id", userID))
			}
			return
		}

		// 更新键盘以反映当前选择 (可选，但用户体验更好)
		// 需要重新生成键盘，标记已选中的按钮
		// newKeyboard := CreateLoraSelectionKeyboardWithSelection(deps.Config.Loras, selectedLoras)
		// editMarkup := tgbotapi.NewEditMessageReplyMarkup(chatID, messageID, newKeyboard)
		// deps.Bot.Send(editMarkup)

		lora := findLoraName(loraID, deps.LoRA)

		callbackResp.Text = fmt.Sprintf("已选择/取消 %s", lora.Name) // 短暂提示
		// deps.Bot.AnswerCallbackQuery(callbackResp)
		if _, err := deps.Bot.Request(callbackResp); err != nil {
			deps.Logger.Error("Failed to answer callback query (lora selected/deselected)", zap.Error(err), zap.Int64("user_id", userID))
		}
		return
	}

	// 处理 LoRA 选择完成
	if data == "lora_done" {
		if currentState.Action != "awaiting_lora_selection" {
			callbackResp.Text = "请先选择 LoRA。"
			// deps.Bot.AnswerCallbackQuery(callbackResp)
			if _, err := deps.Bot.Request(callbackResp); err != nil {
				deps.Logger.Error("Failed to answer callback query (lora done but not awaiting)", zap.Error(err), zap.Int64("user_id", userID))
			}
			return
		}
		if len(currentState.SelectedLoras) == 0 {
			callbackResp.Text = "请至少选择一个 LoRA 风格！"
			// deps.Bot.AnswerCallbackQuery(callbackResp)
			if _, err := deps.Bot.Request(callbackResp); err != nil {
				deps.Logger.Error("Failed to answer callback query (lora done but none selected)", zap.Error(err), zap.Int64("user_id", userID))
			}
			return
		}

		// 清除键盘并显示生成中...
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, fmt.Sprintf("收到！准备使用描述:\n```\n%s\n```\n为选择的 %d 个风格生成图片...", currentState.OriginalCaption, len(currentState.SelectedLoras)))
		editMsg.ParseMode = tgbotapi.ModeMarkdown
		if _, err := deps.Bot.Send(editMsg); err != nil { // 移除键盘
			// 即使编辑失败也要继续尝试生成
			deps.Logger.Error("Failed to edit message for lora_done", zap.Error(err), zap.Int64("user_id", userID))
		}

		// 触发生成逻辑
		go GenerateImagesForUser(currentState, deps)

		deps.StateManager.ClearState(userID) // 清理状态
		// deps.Bot.AnswerCallbackQuery(callbackResp) // 确认回调
		if _, err := deps.Bot.Request(callbackResp); err != nil {
			deps.Logger.Error("Failed to answer callback query (lora done)", zap.Error(err), zap.Int64("user_id", userID))
		}
		return
	}

	// 处理 Caption 确认/编辑
	if data == "caption_confirm" {
		if currentState.Action != "awaiting_caption_confirmation" {
			callbackResp.Text = "Invalid state for caption confirmation."
			// deps.Bot.AnswerCallbackQuery(callbackResp)
			if _, err := deps.Bot.Request(callbackResp); err != nil {
				deps.Logger.Error("Failed to answer callback query (invalid state for caption confirm)", zap.Error(err), zap.Int64("user_id", userID))
			}
			return
		}

		// Update state to proceed to LoRA selection
		currentState.Action = "awaiting_lora_selection"
		deps.StateManager.SetState(userID, currentState)

		// Edit the message to show LoRA selection
		msgText := fmt.Sprintf("Caption confirmed!\n```\n%s\n```\nPlease select LoRA style(s) for generation:", currentState.OriginalCaption)
		keyboard := CreateLoraSelectionKeyboard(deps.LoRA)
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, msgText)
		editMsg.ParseMode = tgbotapi.ModeMarkdown
		editMsg.ReplyMarkup = &keyboard

		if _, err := deps.Bot.Send(editMsg); err != nil {
			deps.Logger.Error("Failed to edit message for LoRA selection after caption confirm", zap.Error(err), zap.Int64("user_id", userID))
			// Attempt to send as new message if edit fails?
		}

		callbackResp.Text = "Caption confirmed."
		// deps.Bot.AnswerCallbackQuery(callbackResp)
		if _, err := deps.Bot.Request(callbackResp); err != nil {
			deps.Logger.Error("Failed to answer callback query (caption confirmed)", zap.Error(err), zap.Int64("user_id", userID))
		}
		return
	}

	if data == "caption_cancel" {
		if currentState.Action != "awaiting_caption_confirmation" {
			// Silently ignore if state doesn't match, maybe user clicked old button
			callbackResp.Text = "Already processed or invalid state."
			// deps.Bot.AnswerCallbackQuery(callbackResp)
			if _, err := deps.Bot.Request(callbackResp); err != nil {
				deps.Logger.Error("Failed to answer callback query (cancel in wrong state)", zap.Error(err), zap.Int64("user_id", userID))
			}
			return
		}

		// Clear state and edit message
		deps.StateManager.ClearState(userID)
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, "Generation cancelled. You can edit the caption and send it as a text message.")
		editMsg.ReplyMarkup = nil // Remove keyboard
		if _, err := deps.Bot.Send(editMsg); err != nil {
			deps.Logger.Error("Failed to edit message for caption cancel", zap.Error(err), zap.Int64("user_id", userID))
		}

		callbackResp.Text = "Cancelled."
		// deps.Bot.AnswerCallbackQuery(callbackResp)
		if _, err := deps.Bot.Request(callbackResp); err != nil {
			deps.Logger.Error("Failed to answer callback query (caption cancelled)", zap.Error(err), zap.Int64("user_id", userID))
		}
		return
	}

	// 未知回调
	deps.Logger.Warn("Unhandled callback data", zap.Int64("user_id", userID), zap.String("data", data))
	callbackResp.Text = "未知操作"
	// deps.Bot.AnswerCallbackQuery(callbackResp)
	if _, err := deps.Bot.Request(callbackResp); err != nil {
		deps.Logger.Error("Failed to answer callback query (unhandled data)", zap.Error(err), zap.Int64("user_id", userID))
	}
}

// 查找 LoRA 名称的辅助函数
func findLoraName(loraID string, loras []LoraConfig) LoraConfig {
	for _, lora := range loras {
		if lora.ID == loraID {
			return lora
		}
	}
	return LoraConfig{} // Fallback
}

// 异步生成图片并发送结果
// GenerateImagesForUser handles the async generation process
func GenerateImagesForUser(userState *UserState, deps BotDeps) {
	userID := userState.UserID
	// Send messages to the user's private chat
	chatID := userState.UserID
	prompt := userState.OriginalCaption
	selectedLoras := userState.SelectedLoras

	// fmt.Println("selectedLoras", selectedLoras)
	// fmt.Println("prompt", prompt)
	// fmt.Println("userID", userID)

	modelEndpoint := deps.Config.APIEndpoints.FluxLora // Get base URL from config

	deps.Logger.Info("Starting image generation",
		zap.Int64("user_id", userID),
		zap.String("prompt", prompt),
		zap.Strings("loras", selectedLoras))

	// Notify user generation started and capture the sent message
	recvMsgConfig := tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Received! Submitting %d image generation task(s)...", len(selectedLoras)))
	recvMsgConfig.ParseMode = tgbotapi.ModeMarkdown
	sentRecvMsg, errRecv := deps.Bot.Send(recvMsgConfig)
	if errRecv != nil {
		deps.Logger.Error("Failed to send initial 'Received!' message", zap.Error(errRecv), zap.Int64("user_id", userID))
		// If this message fails, we might still proceed, but won't be able to delete it.
	}

	generationCtx, cancelGenerations := context.WithTimeout(context.Background(), 10*time.Minute) // Overall timeout for all generations for this user
	defer cancelGenerations()

	var wg sync.WaitGroup                                             // Use WaitGroup to wait for all goroutines
	resultsChan := make(chan generationResultMsg, len(selectedLoras)) // Channel to collect results/errors
	// Channel to collect successfully sent status messages for later deletion
	sentStatusMsgsChan := make(chan tgbotapi.Message, len(selectedLoras))

	for _, loraID := range selectedLoras {
		wg.Add(1)
		go func(lID string) {
			defer wg.Done()
			lora := findLoraName(lID, deps.LoRA)

			// 1. Check Balance (Deduct before submission for simplicity)
			if deps.BalanceManager != nil {
				canProceed, err := deps.BalanceManager.CheckAndDeduct(userID)
				if !canProceed {
					errMsg := fmt.Sprintf("Skipping '%s': Balance insufficient.", lora.Name)
					if err != nil {
						errMsg = fmt.Sprintf("Skipping '%s': %s", lora.Name, err.Error())
					}
					resultsChan <- generationResultMsg{loraName: lora.Name, err: fmt.Errorf(errMsg)}
					// deps.Bot.Send(tgbotapi.NewMessage(chatID, errMsg)) // Notify immediately or collect errors
					return
				}
			}

			// 2. Submit Generation Task
			var loraWeights []falapi.LoraWeight
			var loraNames []string
			for _, lora := range deps.BaseLoRA {
				loraWeights = append(loraWeights, falapi.LoraWeight{Path: lora.URL, Scale: lora.Weight})
				loraNames = append(loraNames, lora.Name)
			}
			for _, lora := range deps.LoRA {
				if lora.ID == lID {
					loraWeights = append(loraWeights, falapi.LoraWeight{Path: lora.URL, Scale: lora.Weight})
					loraNames = append(loraNames, lora.Name)
				}
			}

			requestID, err := deps.FalClient.SubmitGenerationRequest(prompt, loraWeights, loraNames, deps.Config.DefaultGenerationSettings.ImageSize, deps.Config.DefaultGenerationSettings.NumInferenceSteps, deps.Config.DefaultGenerationSettings.GuidanceScale)
			if err != nil {
				deps.Logger.Error("Failed to submit generation request", zap.Error(err), zap.Int64("user_id", userID), zap.String("lora", lID))
				errMsg := fmt.Sprintf("Failed to submit task for '%s': %s", lora.Name, err.Error())
				// Consider refunding balance if deducted
				// if deps.BalanceManager != nil { deps.BalanceManager.AddBalance(userID, deps.Config.Balance.CostPerGeneration) }
				resultsChan <- generationResultMsg{loraName: lora.Name, err: fmt.Errorf(errMsg)}
				return
			}

			deps.Logger.Info("Submitted generation task", zap.Int64("user_id", userID), zap.String("lora", lID), zap.String("request_id", requestID))
			// Send status message and try to capture the result for deletion
			submitMsgConfig := tgbotapi.NewMessage(chatID, fmt.Sprintf("⏳ Task for '%s' submitted (ID: ...%s). Waiting for result...", lora.Name, truncateID(requestID)))
			submitMsgConfig.ParseMode = tgbotapi.ModeMarkdown
			sentSubmitMsg, errSend := deps.Bot.Send(submitMsgConfig)
			if errSend != nil {
				deps.Logger.Error("Failed to send 'Task submitted' status message", zap.Error(errSend), zap.Int64("user_id", userID), zap.String("lora", lID))
				// Don't send to channel if sending failed
			} else {
				// Send successfully sent message to the channel
				sentStatusMsgsChan <- sentSubmitMsg
			}

			// 3. Poll for Result (with timeout specific to this task)
			pollInterval := 10 * time.Second // Adjust poll interval
			generationResult, err := deps.FalClient.PollForResult(generationCtx, requestID, modelEndpoint, pollInterval)

			if err != nil {
				deps.Logger.Error("Polling failed or generation failed", zap.Error(err), zap.Int64("user_id", userID), zap.String("lora", lID), zap.String("request_id", requestID))
				errMsg := fmt.Sprintf("Failed generation for '%s': %s", lora.Name, err.Error())
				// Balance already deducted, no refund needed for failed generation per current logic
				resultsChan <- generationResultMsg{loraName: lora.Name, err: fmt.Errorf(errMsg)}
				return
			}

			// 4. Process successful result
			deps.Logger.Info("Generation completed successfully", zap.Int64("user_id", userID), zap.String("lora", lID), zap.String("request_id", requestID))
			resultsChan <- generationResultMsg{loraName: lora.Name, result: generationResult, prompt: prompt}

		}(loraID)
	}

	// Wait for all goroutines to finish and close channels
	go func() {
		wg.Wait()
		close(resultsChan)
		close(sentStatusMsgsChan) // Close the status message channel too
	}()

	numSuccess := 0
	numFailed := 0
	var errorMessages []string
	// Collect successfully sent status messages from the channel
	var statusMessagesToDelete []tgbotapi.Message
	for msg := range sentStatusMsgsChan {
		statusMessagesToDelete = append(statusMessagesToDelete, msg)
	}

	for msg := range resultsChan {
		if msg.err != nil {
			numFailed++
			errorMessages = append(errorMessages, msg.err.Error())
			// Send individual error message immediately? Or collect?
			deps.Bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ %s", msg.err.Error())))
		} else if msg.result != nil && len(msg.result.Images) > 0 {
			numSuccess++
			// Send the image(s) as a media group
			imgURL := msg.result.Images[0].URL // Get the URL of the first image for potential later use (e.g., buttons)
			mediaGroup := []interface{}{}
			for i, image := range msg.result.Images {
				photo := tgbotapi.NewInputMediaPhoto(tgbotapi.FileURL(image.URL))
				// Set caption only for the first photo in the group
				// if i == 0 {
				// 	photo.Caption = fmt.Sprintf("✅ '%s'\nPrompt: %s", msg.loraName, msg.prompt)
				// 	// Optionally add ParseMode if needed: photo.ParseMode = tgbotapi.ModeMarkdown
				// }
				mediaGroup = append(mediaGroup, photo)
			}

			// Use NewMediaGroup instead of NewMediaGroupConfig
			mediaGroupConfig := tgbotapi.NewMediaGroup(chatID, mediaGroup)
			_, err := deps.Bot.SendMediaGroup(mediaGroupConfig)

			if err != nil {
				deps.Logger.Error("Failed to send generated photo group", zap.Error(err), zap.Int64("user_id", userID), zap.String("lora", msg.loraName))
				// Also report send failure to user
				deps.Bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Successfully generated '%s' image(s), but failed to send them.", msg.loraName)))
			} else {
				// Image sent successfully, maybe send the caption with button separately?
				// Send the caption and button as a separate message AFTER the media group
				caption_msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Details for '%s' (Seed: %d)", msg.loraName, msg.result.Seed))
				caption_msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
					tgbotapi.NewInlineKeyboardRow(
						// Use the first image's URL for the button
						tgbotapi.NewInlineKeyboardButtonURL(fmt.Sprintf("🔍 View on Fal.ai (%s)", msg.loraName), imgURL),
					),
				)
				_, err := deps.Bot.Send(caption_msg)
				if err != nil {
					deps.Logger.Error("Failed to send caption message after media group", zap.Error(err), zap.Int64("user_id", userID), zap.String("lora", msg.loraName))
				}
			}
		} else {
			// Handle case where result is somehow nil or has no images despite no error
			numFailed++
			errMsg := fmt.Sprintf("Task for '%s' completed but returned no image.", msg.loraName)
			errorMessages = append(errorMessages, errMsg)
			deps.Bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ %s", errMsg)))
		}
	}

	// Delete the collected status messages
	for _, msgToDelete := range statusMessagesToDelete {
		deleteCfg := tgbotapi.DeleteMessageConfig{ChatID: msgToDelete.Chat.ID, MessageID: msgToDelete.MessageID}
		_, errDel := deps.Bot.Request(deleteCfg)
		if errDel != nil {
			deps.Logger.Warn("Failed to delete submit status message", zap.Error(errDel), zap.Int64("chat_id", msgToDelete.Chat.ID), zap.Int("message_id", msgToDelete.MessageID))
		}
	}
	// Delete the initial "Received!" message if it was sent successfully
	if errRecv == nil {
		deleteCfg := tgbotapi.DeleteMessageConfig{ChatID: sentRecvMsg.Chat.ID, MessageID: sentRecvMsg.MessageID}
		_, errDel := deps.Bot.Request(deleteCfg)
		if errDel != nil {
			deps.Logger.Warn("Failed to delete receive status message", zap.Error(errDel), zap.Int64("chat_id", sentRecvMsg.Chat.ID), zap.Int("message_id", sentRecvMsg.MessageID))
		}
	}

	// Send summary message
	summary := fmt.Sprintf("🏁 Generation finished: %d successful, %d failed.", numSuccess, numFailed)
	// if numFailed > 0 {
	//     summary += "\nErrors:\n- " + strings.Join(errorMessages, "\n- ")
	// }
	deps.Bot.Send(tgbotapi.NewMessage(chatID, summary))

	deps.Logger.Info("Finished all generation tasks for user", zap.Int64("user_id", userID), zap.Int("success", numSuccess), zap.Int("failed", numFailed))
}

// Helper type for collecting results from goroutines
type generationResultMsg struct {
	loraName string
	result   *falapi.GenerateResponse
	err      error
	prompt   string // Pass prompt for captioning result
}

// Helper to truncate long request IDs for display
func truncateID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}
