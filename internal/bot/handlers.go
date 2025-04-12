package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	st "github.com/nerdneilsfield/telegram-fal-bot/internal/storage"
	falapi "github.com/nerdneilsfield/telegram-fal-bot/pkg/falapi"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// --- Constants for User Feedback ---
const (
	errMsgGeneric             = "❌ 处理您的请求时发生内部错误，请稍后再试或联系管理员。"
	errMsgStateExpired        = "⏳ 操作已过期或无效，请重新开始。"
	errMsgInsufficientBalance = "💰 余额不足。需要 %.2f 点，当前 %.2f 点。"
	errMsgInvalidConfigInput  = "⚠️ 无效输入。请检查格式或范围。"
)

// Helper to send generic error message and log details
func sendGenericError(chatID int64, userID int64, operation string, err error, deps BotDeps) {
	deps.Logger.Error("Operation failed", zap.String("operation", operation), zap.Error(err), zap.Int64("user_id", userID))
	// Send generic message to user
	reply := tgbotapi.NewMessage(chatID, errMsgGeneric)
	deps.Bot.Send(reply)
}

// Helper to edit a message with generic error
func editWithGenericError(chatID int64, messageID int, userID int64, operation string, err error, deps BotDeps) {
	deps.Logger.Error("Operation failed", zap.String("operation", operation), zap.Error(err), zap.Int64("user_id", userID))
	edit := tgbotapi.NewEditMessageText(chatID, messageID, errMsgGeneric)
	edit.ReplyMarkup = nil // Clear keyboard on error
	deps.Bot.Send(edit)
}

func HandleUpdate(update tgbotapi.Update, deps BotDeps) {
	defer func() {
		if r := recover(); r != nil {
			errMsg := fmt.Sprintf("%v", r)
			stackTrace := string(debug.Stack())
			deps.Logger.Error("Panic recovered in HandleUpdate", zap.Any("panic_value", errMsg), zap.String("stack", stackTrace))

			// Try to notify user/admin about the panic
			var chatID int64
			var userID int64
			if update.Message != nil {
				chatID = update.Message.Chat.ID
				userID = update.Message.From.ID
			} else if update.CallbackQuery != nil {
				userID = update.CallbackQuery.From.ID
				if update.CallbackQuery.Message != nil {
					chatID = update.CallbackQuery.Message.Chat.ID
				}
			}

			if chatID != 0 {
				if deps.Authorizer.IsAdmin(userID) {
					// Send detailed panic to admin
					detailedMsg := fmt.Sprintf("☢️ PANIC RECOVERED ☢️\nUser: %d\nError: %s\n\nTraceback:\n```\n%s\n```", userID, errMsg, stackTrace)
					const maxLen = 4090
					if len(detailedMsg) > maxLen {
						detailedMsg = detailedMsg[:maxLen] + "\n...(truncated)```"
					}
					msg := tgbotapi.NewMessage(chatID, detailedMsg)
					// Use ModeMarkdown for panic message as well, simpler
					msg.ParseMode = tgbotapi.ModeMarkdown
					deps.Bot.Send(msg)
				} else {
					// Send generic error to non-admin
					deps.Bot.Send(tgbotapi.NewMessage(chatID, errMsgGeneric))
				}
			}
		}
	}()

	if update.Message != nil {
		HandleMessage(update.Message, deps)
	} else if update.CallbackQuery != nil {
		HandleCallbackQuery(update.CallbackQuery, deps)
	}
}

func HandleMessage(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID

	// DO NOT Clear state at the beginning. Clear it specifically when needed.

	// 命令处理
	if message.IsCommand() {
		switch message.Command() {
		case "start":
			reply := tgbotapi.NewMessage(chatID, "欢迎使用 Flux LoRA 图片生成 Bot！\n发送图片进行描述和生成，或直接发送描述文本生成图片。\n使用 /balance 查看余额。\n使用 /loras 查看可用风格。\n使用 /myconfig 查看或修改您的生成参数。\n使用 /version 查看版本信息。")
			// Switch back to ModeMarkdown
			reply.ParseMode = tgbotapi.ModeMarkdown
			deps.Bot.Send(reply)
		case "help": // Handle /help command
			HandleHelpCommand(chatID, deps) // Help command now handles its own ParseMode
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
			// Get visible LoRAs for the user
			visibleLoras := GetUserVisibleLoras(userID, deps)

			var loraList strings.Builder
			if len(visibleLoras) > 0 {
				loraList.WriteString("可用的 LoRA 风格:\n")
				for _, lora := range visibleLoras {
					// Use backticks for LoRA names, should render as code in ModeMarkdown
					loraList.WriteString(fmt.Sprintf("- `%s`\n", lora.Name))
				}
			} else {
				loraList.WriteString("当前没有可用的 LoRA 风格。")
			}

			// Admins can also see BaseLoRAs
			if deps.Authorizer.IsAdmin(userID) && len(deps.BaseLoRA) > 0 {
				loraList.WriteString("\nBase LoRA 风格 (仅管理员可见):\n")
				for _, lora := range deps.BaseLoRA {
					loraList.WriteString(fmt.Sprintf("- `%s`\n", lora.Name))
				}
			}

			reply := tgbotapi.NewMessage(chatID, loraList.String())
			// Switch back to ModeMarkdown
			reply.ParseMode = tgbotapi.ModeMarkdown
			deps.Bot.Send(reply)

		case "version":
			reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("当前版本: %s\n构建日期: %s\nGo 版本: %s", deps.Version, deps.BuildDate, runtime.Version()))
			// Switch back to ModeMarkdown
			reply.ParseMode = tgbotapi.ModeMarkdown
			deps.Bot.Send(reply)

		case "myconfig":
			HandleMyConfigCommand(message, deps) // Config command handles its own ParseMode

		case "set":
			if !deps.Authorizer.IsAdmin(userID) {
				deps.Bot.Send(tgbotapi.NewMessage(chatID, "只有管理员才能使用此命令。"))
				return
			}
			deps.Bot.Send(tgbotapi.NewMessage(chatID, "管理员设置功能正在开发中..."))

		case "cancel":
			state, exists := deps.StateManager.GetState(userID)
			if exists {
				deps.StateManager.ClearState(userID)
				deps.Logger.Info("User cancelled operation via /cancel", zap.Int64("user_id", userID), zap.String("state", state.Action))
				// Try to edit the original message associated with the state if possible
				if state.ChatID != 0 && state.MessageID != 0 {
					edit := tgbotapi.NewEditMessageText(state.ChatID, state.MessageID, "✅ 操作已取消。")
					edit.ReplyMarkup = nil
					deps.Bot.Send(edit)
				} else {
					// Fallback if state didn't have message context
					reply := tgbotapi.NewMessage(chatID, "✅ 当前操作已取消。")
					deps.Bot.Send(reply)
				}
			} else {
				deps.Bot.Send(tgbotapi.NewMessage(chatID, "当前没有进行中的操作可以取消。"))
			}

		default:
			deps.Bot.Send(tgbotapi.NewMessage(chatID, "未知命令。"))
		}
		return
	}

	// 图片消息处理
	if message.Photo != nil && len(message.Photo) > 0 {
		// Clear any previous state before starting a new action with a photo
		deps.StateManager.ClearState(userID)
		HandlePhotoMessage(message, deps)
		return
	}

	// 文本消息处理 (Prompt or potentially config update)
	if message.Text != "" {
		state, exists := deps.StateManager.GetState(userID)
		if exists && strings.HasPrefix(state.Action, "awaiting_config_") {
			// Let HandleConfigUpdateInput manage state clearing on completion/error
			HandleConfigUpdateInput(message, state, deps)
		} else {
			// Clear any previous state before starting a new action with text
			deps.StateManager.ClearState(userID)
			HandleTextMessage(message, deps) // Treats as prompt
		}
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
		deps.Bot.Send(tgbotapi.NewMessage(chatID, "⚠️ 无法处理图片：未找到图片数据。")) // Improved feedback
		return
	}
	photo := message.Photo[len(message.Photo)-1] // Highest resolution
	fileConfig := tgbotapi.FileConfig{FileID: photo.FileID}
	file, err := deps.Bot.GetFile(fileConfig)
	if err != nil {
		sendGenericError(chatID, userID, "GetFile", err, deps)
		return
	}
	imageURL := file.Link(deps.Bot.Token)

	// 2. Send initial "Submitting..." message
	var msgIDToEdit int
	waitMsg := tgbotapi.NewMessage(chatID, "⏳ 正在提交图片进行描述...") // Updated text
	sentMsg, err := deps.Bot.Send(waitMsg)
	if err == nil && sentMsg.MessageID != 0 {
		msgIDToEdit = sentMsg.MessageID
	} else if err != nil {
		deps.Logger.Error("Failed to send initial wait message for captioning", zap.Error(err), zap.Int64("user_id", userID))
	}

	// 3. Start captioning process in a Goroutine
	go func(imgURL string, originalChatID int64, originalUserID int64, editMsgID int) {
		captionEndpoint := deps.Config.APIEndpoints.FlorenceCaption // Get caption endpoint from config
		pollInterval := 5 * time.Second                             // Adjust interval as needed
		captionTimeout := 2 * time.Minute                           // Timeout for captioning

		// 3a. Submit caption request
		requestID, err := deps.FalClient.SubmitCaptionRequest(imgURL)
		if err != nil {
			// Log detailed error, send more specific error to user if possible
			errText := fmt.Sprintf("❌ 获取图片描述失败: %s", err.Error())
			if errors.Is(err, context.DeadlineExceeded) {
				errText = "❌ 获取图片描述超时，请稍后重试。"
			}
			deps.Logger.Error("Polling/captioning failed", zap.Error(err), zap.Int64("user_id", originalUserID), zap.String("request_id", requestID))
			if editMsgID != 0 {
				edit := tgbotapi.NewEditMessageText(originalChatID, editMsgID, errText)
				edit.ReplyMarkup = nil
				deps.Bot.Send(edit)
			} else {
				deps.Bot.Send(tgbotapi.NewMessage(originalChatID, errText))
			}
			return
		}

		deps.Logger.Info("Submitted caption task", zap.Int64("user_id", originalUserID), zap.String("request_id", requestID))
		statusUpdate := fmt.Sprintf("⏳ 图片描述任务已提交 (ID: ...%s)。正在等待结果...", truncateID(requestID))
		if editMsgID != 0 {
			deps.Bot.Send(tgbotapi.NewEditMessageText(originalChatID, editMsgID, statusUpdate))
		}

		// 3b. Poll for caption result
		ctx, cancel := context.WithTimeout(context.Background(), captionTimeout)
		defer cancel()
		captionText, err := deps.FalClient.PollForCaptionResult(ctx, requestID, captionEndpoint, pollInterval)

		if err != nil {
			// Log detailed error, provide more specific error if possible
			errText := fmt.Sprintf("❌ 获取图片描述失败: %s", err.Error())
			if errors.Is(err, context.DeadlineExceeded) {
				errText = "❌ 获取图片描述超时，请稍后重试。"
			}
			deps.Logger.Error("Polling/captioning failed", zap.Error(err), zap.Int64("user_id", originalUserID), zap.String("request_id", requestID))
			if editMsgID != 0 {
				edit := tgbotapi.NewEditMessageText(originalChatID, editMsgID, errText)
				edit.ReplyMarkup = nil
				deps.Bot.Send(edit)
			} else {
				deps.Bot.Send(tgbotapi.NewMessage(originalChatID, errText))
			}
			return
		}

		deps.Logger.Info("Caption received successfully", zap.Int64("user_id", originalUserID), zap.String("request_id", requestID), zap.String("caption", captionText))

		// 4. Caption Success: Store state and ask for confirmation
		newState := &UserState{
			UserID:          originalUserID,
			ChatID:          originalChatID,
			MessageID:       editMsgID,
			Action:          "awaiting_caption_confirmation",
			OriginalCaption: captionText,
			SelectedLoras:   []string{},
		}
		deps.StateManager.SetState(originalUserID, newState)

		// 5. Send caption and confirmation keyboard (editing the status message)
		// Use backticks for caption, ModeMarkdown should handle it
		msgText := fmt.Sprintf("✅ Caption received:\n```\n%s\n```\nConfirm generation with this caption, or cancel?", captionText)
		confirmationKeyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ Confirm Generation", "caption_confirm"),
				tgbotapi.NewInlineKeyboardButtonData("❌ Cancel", "caption_cancel"),
			),
		)

		var finalMsg tgbotapi.Chattable
		if editMsgID != 0 {
			editMsg := tgbotapi.NewEditMessageText(originalChatID, editMsgID, msgText)
			// Switch back to ModeMarkdown
			editMsg.ParseMode = tgbotapi.ModeMarkdown
			editMsg.ReplyMarkup = &confirmationKeyboard
			finalMsg = editMsg
		} else {
			newMsg := tgbotapi.NewMessage(originalChatID, msgText)
			// Switch back to ModeMarkdown
			newMsg.ParseMode = tgbotapi.ModeMarkdown
			newMsg.ReplyMarkup = &confirmationKeyboard
			finalMsg = newMsg
		}
		_, err = deps.Bot.Send(finalMsg)
		if err != nil {
			deps.Logger.Error("Failed to send caption result & confirmation keyboard", zap.Error(err), zap.Int64("user_id", originalUserID))
		}

	}(imageURL, chatID, userID, msgIDToEdit)

	// Return immediately, the goroutine handles the rest
}

func HandleTextMessage(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID

	// Send message indicating LoRA selection will start
	waitMsg := tgbotapi.NewMessage(chatID, "⏳ Got it! Please select LoRA styles for your prompt...")
	sentMsg, err := deps.Bot.Send(waitMsg)
	if err != nil {
		deps.Logger.Error("Failed to send initial wait message for text prompt", zap.Error(err), zap.Int64("user_id", userID))
	}
	msgIDForKeyboard := 0 // Initialize to 0
	if sentMsg.MessageID != 0 {
		msgIDForKeyboard = sentMsg.MessageID // Use the new message ID for the keyboard
	}

	// Set state and show LoRA selection
	newState := &UserState{
		UserID:          userID,
		ChatID:          chatID,
		MessageID:       msgIDForKeyboard,
		Action:          "awaiting_lora_selection",
		OriginalCaption: message.Text,
		SelectedLoras:   []string{},
	}
	deps.StateManager.SetState(userID, newState)

	// Edit the bot's message (if sent successfully) to show LoRA keyboard
	if msgIDForKeyboard != 0 {
		// SendLoraSelectionKeyboard now handles its own ParseMode
		SendLoraSelectionKeyboard(chatID, msgIDForKeyboard, newState, deps, true)
	} else {
		// Fallback if sending waitMsg failed? Maybe send a new message with keyboard.
		deps.Logger.Warn("Could not send wait message, sending keyboard as new message", zap.Int64("user_id", userID))
		SendLoraSelectionKeyboard(chatID, 0, newState, deps, false) // Send as new message
	}
}

func HandleCallbackQuery(callbackQuery *tgbotapi.CallbackQuery, deps BotDeps) {
	userID := callbackQuery.From.ID
	var chatID int64
	var messageID int
	if callbackQuery.Message != nil {
		chatID = callbackQuery.Message.Chat.ID
		messageID = callbackQuery.Message.MessageID
	} else {
		// ... (error handling for nil message) ...
		deps.Logger.Error("Callback query message is nil", zap.Int64("user_id", userID), zap.String("data", callbackQuery.Data))
		answer := tgbotapi.NewCallback(callbackQuery.ID, "错误：无法处理此操作。")
		deps.Bot.Request(answer)
		return
	}
	data := callbackQuery.Data

	deps.Logger.Info("Callback received", zap.Int64("user_id", userID), zap.String("data", data), zap.Int64("chat_id", chatID), zap.Int("message_id", messageID))

	answer := tgbotapi.NewCallback(callbackQuery.ID, "") // Prepare default answer

	// --- Configuration Callbacks ---
	if strings.HasPrefix(data, "config_") {
		HandleConfigCallback(callbackQuery, deps)
		return
	}

	// --- Lora Selection Callbacks ---
	state, ok := deps.StateManager.GetState(userID)
	if !ok {
		// ... (error handling for no state) ...
		deps.Logger.Warn("Received callback but no state found or state expired", zap.Int64("user_id", userID), zap.String("data", data))
		answer.Text = errMsgStateExpired
		deps.Bot.Request(answer)
		edit := tgbotapi.NewEditMessageText(chatID, messageID, errMsgStateExpired)
		edit.ReplyMarkup = nil
		deps.Bot.Send(edit)
		return
	}

	// Ensure state has chat/message ID
	if state.ChatID == 0 || state.MessageID == 0 {
		deps.Logger.Error("State is missing ChatID or MessageID during callback", zap.Int64("userID", userID), zap.Int64("stateChatID", state.ChatID), zap.Int("stateMessageID", state.MessageID))
		// Attempt to use current callback message info as fallback? Risky.
		// For now, treat as error.
		answer.Text = "内部状态错误，请重试。"
		deps.Bot.Request(answer)
		edit := tgbotapi.NewEditMessageText(chatID, messageID, "内部状态错误，请重试。") // Edit the current message
		edit.ReplyMarkup = nil
		deps.Bot.Send(edit)
		deps.StateManager.ClearState(userID)
		return
	}

	switch state.Action {
	case "awaiting_lora_selection": // Step 1: Selecting Standard LoRAs
		if strings.HasPrefix(data, "lora_select_") {
			loraID := strings.TrimPrefix(data, "lora_select_")
			// Need BotDeps to find the LoRA details by ID
			allLoras := append(deps.LoRA) // Only standard LoRAs are selectable here
			selectedLora := findLoraByID(loraID, allLoras)

			if selectedLora.ID == "" { // Not found
				// ... (error handling for invalid lora ID) ...
				answer.Text = "错误：无效的 LoRA 选择"
				deps.Bot.Request(answer)
				deps.Logger.Warn("Invalid standard lora ID selected", zap.String("loraID", loraID), zap.Int64("user_id", userID))
				return
			}

			// Toggle selection using Lora Name in state.SelectedLoras
			found := false
			newSelection := []string{}
			for _, name := range state.SelectedLoras {
				if name == selectedLora.Name {
					found = true
				} else {
					newSelection = append(newSelection, name)
				}
			}
			if !found {
				newSelection = append(newSelection, selectedLora.Name)
			}
			state.SelectedLoras = newSelection
			deps.StateManager.SetState(userID, state) // Save updated selection

			// Update keyboard
			ansText := fmt.Sprintf("已选标准: %s", strings.Join(state.SelectedLoras, ", "))
			if len(state.SelectedLoras) == 0 {
				ansText = "请选择至少一个标准 LoRA"
			}
			answer.Text = ansText
			deps.Bot.Request(answer)
			// Re-send the standard LoRA keyboard with updated selections
			// SendLoraSelectionKeyboard handles ParseMode internally now
			SendLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true)

		} else if data == "lora_standard_done" { // Finished selecting standard LoRAs
			if len(state.SelectedLoras) == 0 {
				answer.Text = "请至少选择一个标准 LoRA！"
				deps.Bot.Request(answer)
				return
			}
			answer.Text = "请选择一个 Base LoRA (可选)"
			deps.Bot.Request(answer)

			// Update state and show Base LoRA keyboard
			state.Action = "awaiting_base_lora_selection"
			deps.StateManager.SetState(userID, state)
			// SendBaseLoraSelectionKeyboard handles ParseMode internally now
			SendBaseLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true) // New function needed

		} else if data == "lora_cancel" {
			// ... (cancel handling) ...
			answer.Text = "操作已取消"
			deps.Bot.Request(answer)
			deps.StateManager.ClearState(userID)
			edit := tgbotapi.NewEditMessageText(state.ChatID, state.MessageID, "操作已取消。")
			edit.ReplyMarkup = nil // Clear keyboard
			deps.Bot.Send(edit)
		} else if data == "lora_noop" {
			// Do nothing, just answer the callback
			deps.Bot.Request(answer)
		} else {
			answer.Text = "未知操作"
			deps.Bot.Request(answer)
		}

	case "awaiting_base_lora_selection": // Step 2: Selecting (optional) Base LoRA
		if strings.HasPrefix(data, "base_lora_select_") {
			loraID := strings.TrimPrefix(data, "base_lora_select_")
			// Find the selected Base LoRA by ID
			selectedBaseLora := findLoraByID(loraID, deps.BaseLoRA)

			if selectedBaseLora.ID == "" { // Not found
				answer.Text = "错误：无效的 Base LoRA 选择"
				deps.Bot.Request(answer)
				deps.Logger.Warn("Invalid base lora ID selected", zap.String("loraID", loraID), zap.Int64("user_id", userID))
				return
			}

			// Update state with the selected Base LoRA Name
			if state.SelectedBaseLoraName == selectedBaseLora.Name {
				state.SelectedBaseLoraName = "" // Deselect if clicked again
				answer.Text = "已取消选择 Base LoRA"
			} else {
				state.SelectedBaseLoraName = selectedBaseLora.Name
				answer.Text = fmt.Sprintf("已选 Base: %s", state.SelectedBaseLoraName)
			}
			deps.StateManager.SetState(userID, state)
			deps.Bot.Request(answer)
			// Update keyboard to show selection
			// SendBaseLoraSelectionKeyboard handles ParseMode internally now
			SendBaseLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true)

		} else if data == "base_lora_skip" {
			state.SelectedBaseLoraName = ""
			deps.StateManager.SetState(userID, state)
			answer.Text = "已跳过选择 Base LoRA"
			deps.Bot.Request(answer)
			// Update keyboard
			// SendBaseLoraSelectionKeyboard handles ParseMode internally now
			SendBaseLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true)

		} else if data == "lora_confirm_generate" {
			// Final confirmation step
			if len(state.SelectedLoras) == 0 {
				// Should not happen if previous step enforced selection, but check again
				answer.Text = "错误：没有选择任何标准 LoRA。"
				deps.Bot.Request(answer)
				return
			}

			answer.Text = "正在提交生成请求..."
			deps.Bot.Request(answer)

			// Build confirmation message using backticks (should work in ModeMarkdown)
			confirmText := fmt.Sprintf("⏳ 准备生成 %d 个组合...\n标准 LoRA: `%s`\n", len(state.SelectedLoras), strings.Join(state.SelectedLoras, "`, `"))
			if state.SelectedBaseLoraName != "" {
				confirmText += fmt.Sprintf("Base LoRA: `%s`\n", state.SelectedBaseLoraName)
			}

			confirmText += fmt.Sprintf("Prompt: ```\n%s\n```", state.OriginalCaption)

			edit := tgbotapi.NewEditMessageText(state.ChatID, state.MessageID, confirmText)
			// Switch back to ModeMarkdown
			edit.ParseMode = tgbotapi.ModeMarkdown
			edit.ReplyMarkup = nil // Clear keyboard before starting generation
			deps.Bot.Send(edit)

			// Start generation in background
			go GenerateImagesForUser(state, deps)

		} else if data == "base_lora_cancel" { // Option to cancel at base lora step
			answer.Text = "操作已取消"
			deps.Bot.Request(answer)
			deps.StateManager.ClearState(userID)
			edit := tgbotapi.NewEditMessageText(state.ChatID, state.MessageID, "操作已取消。")
			edit.ReplyMarkup = nil // Clear keyboard
			deps.Bot.Send(edit)
		} else if data == "lora_noop" { // Keep noop for potential placeholders in base keyboard
			deps.Bot.Request(answer)
		} else {
			answer.Text = "未知操作"
			deps.Bot.Request(answer)
		}

	// ... handle other actions like awaiting_config_value ...
	default:
		deps.Logger.Warn("Callback received for unhandled action", zap.String("action", state.Action), zap.Int64("user_id", userID), zap.String("data", data))
		answer.Text = "未知状态或操作"
		deps.Bot.Request(answer)
	}
}

// Handles callbacks starting with "config_"
func HandleConfigCallback(callbackQuery *tgbotapi.CallbackQuery, deps BotDeps) {
	userID := callbackQuery.From.ID
	// Ensure message context exists
	if callbackQuery.Message == nil {
		deps.Logger.Error("Config callback query message is nil", zap.Int64("user_id", userID), zap.String("data", callbackQuery.Data))
		answer := tgbotapi.NewCallback(callbackQuery.ID, "Error: Message context missing.")
		deps.Bot.Request(answer)
		return
	}
	chatID := callbackQuery.Message.Chat.ID
	messageID := callbackQuery.Message.MessageID
	data := callbackQuery.Data

	answer := tgbotapi.NewCallback(callbackQuery.ID, "") // Prepare answer

	// Get current config or initialize a new one
	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		deps.Logger.Error("Failed to get user config during callback", zap.Error(err), zap.Int64("user_id", userID))
		answer.Text = "❌ 获取配置出错"
		deps.Bot.Request(answer)
		return
	}
	if userCfg == nil {
		userCfg = &st.UserGenerationConfig{UserID: userID}
	}

	var updateErr error
	var newStateAction string
	var promptText string
	var keyboard *tgbotapi.InlineKeyboardMarkup // Keyboard for text input prompt

	switch data {
	case "config_set_imagesize":
		answer.Text = "选择图片尺寸"
		deps.Bot.Request(answer) // Answer first
		sizes := []string{"square", "portrait_16_9", "landscape_16_9", "portrait_4_3", "landscape_4_3"}
		var rows [][]tgbotapi.InlineKeyboardButton
		// Determine current value to potentially highlight?
		currentSize := deps.Config.DefaultGenerationSettings.ImageSize
		if userCfg.ImageSize != nil {
			currentSize = *userCfg.ImageSize
		}
		for _, size := range sizes {
			buttonText := size
			if size == currentSize {
				buttonText = "➡️ " + size // Indicate current selection
			}
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(buttonText, "config_imagesize_"+size),
			))
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("返回配置主菜单", "config_back_main"),
		))
		kbd := tgbotapi.NewInlineKeyboardMarkup(rows...)                     // Create keyboard value
		keyboard = &kbd                                                      // Assign the address of the keyboard to the pointer
		edit := tgbotapi.NewEditMessageText(chatID, messageID, "请选择新的图片尺寸:") // Update text as well
		edit.ReplyMarkup = keyboard                                          // Use the pointer
		deps.Bot.Send(edit)
		return // Waiting for selection

	case "config_set_infsteps":
		answer.Text = "请输入推理步数 (1-50)"
		newStateAction = "awaiting_config_infsteps"
		promptText = "请输入您想要的推理步数 (1-50 之间的整数)。\n发送其他任何文本或使用 /cancel 将取消设置。"
		// Create keyboard with a cancel button
		cancelButtonRow := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("❌ 取消设置", "config_cancel_input"))
		kbd := tgbotapi.NewInlineKeyboardMarkup(cancelButtonRow)
		keyboard = &kbd

	case "config_set_guidscale":
		answer.Text = "请输入 Guidance Scale (0-15)"
		newStateAction = "awaiting_config_guidscale"
		promptText = "请输入您想要的 Guidance Scale (0-15 之间的数字，例如 7.5)。\n发送其他任何文本或使用 /cancel 将取消设置。"
		// Create keyboard with a cancel button
		cancelButtonRow := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("❌ 取消设置", "config_cancel_input"))
		kbd := tgbotapi.NewInlineKeyboardMarkup(cancelButtonRow)
		keyboard = &kbd

	case "config_set_numimages":
		answer.Text = "请输入生成数量 (1-10)" // (Range can be adjusted)
		newStateAction = "awaiting_config_numimages"
		promptText = "请输入您想要的每次生成图片的数量 (1-10 之间的整数)。\n发送其他任何文本或使用 /cancel 将取消设置。"
		// Add cancel button
		cancelButtonRow := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("❌ 取消设置", "config_cancel_input"))
		kbd := tgbotapi.NewInlineKeyboardMarkup(cancelButtonRow)
		keyboard = &kbd

	case "config_reset_defaults":
		result := deps.DB.Delete(&st.UserGenerationConfig{}, "user_id = ?", userID)
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			sendGenericError(chatID, userID, "ResetConfig", result.Error, deps) // Use helper
			answer.Text = "❌ 重置配置失败"
		} else {
			deps.Logger.Info("User config reset to defaults", zap.Int64("user_id", userID))
			answer.Text = "✅ 配置已恢复为默认设置"
			// Create a *basic* message context for editing
			syntheticMsg := &tgbotapi.Message{
				MessageID: messageID,
				From:      callbackQuery.From,
				Chat:      callbackQuery.Message.Chat,
			}
			HandleMyConfigCommand(syntheticMsg, deps)
		}
		deps.Bot.Request(answer)
		deps.StateManager.ClearState(userID)
		return

	case "config_back_main":
		answer.Text = "返回主菜单"
		deps.Bot.Request(answer)
		syntheticMsg := &tgbotapi.Message{
			MessageID: messageID,
			From:      callbackQuery.From,
			Chat:      callbackQuery.Message.Chat,
		}
		HandleMyConfigCommand(syntheticMsg, deps)
		deps.StateManager.ClearState(userID)
		return

	case "config_cancel_input": // Handle the cancel button press
		answer.Text = "取消输入"
		deps.Bot.Request(answer)
		deps.StateManager.ClearState(userID)
		deps.Logger.Info("User cancelled config input via button", zap.Int64("user_id", userID))
		// Go back to the main config view
		syntheticMsg := &tgbotapi.Message{
			MessageID: messageID,
			From:      callbackQuery.From,
			Chat:      callbackQuery.Message.Chat,
		}
		HandleMyConfigCommand(syntheticMsg, deps)
		return

	default:
		if strings.HasPrefix(data, "config_imagesize_") {
			size := strings.TrimPrefix(data, "config_imagesize_")
			validSizes := map[string]bool{"square": true, "portrait_16_9": true, "landscape_16_9": true, "portrait_4_3": true, "landscape_4_3": true}
			if !validSizes[size] {
				deps.Logger.Warn("Invalid image size received in callback", zap.String("size", size), zap.Int64("user_id", userID))
				answer.Text = "无效的尺寸"
				deps.Bot.Request(answer)
				return
			}
			userCfg.ImageSize = &size
			updateErr = st.SetUserGenerationConfig(deps.DB, userID, *userCfg)
			if updateErr == nil {
				answer.Text = fmt.Sprintf("✅ 图片尺寸已设为 %s", size)
				syntheticMsg := &tgbotapi.Message{
					MessageID: messageID,
					From:      callbackQuery.From,
					Chat:      callbackQuery.Message.Chat,
				}
				HandleMyConfigCommand(syntheticMsg, deps)
			} else {
				// Log detail, give generic feedback
				deps.Logger.Error("Failed to update image size", zap.Error(updateErr), zap.Int64("user_id", userID), zap.String("size", size))
				answer.Text = "❌ 更新图片尺寸失败"
			}
			deps.Bot.Request(answer)
			deps.StateManager.ClearState(userID)
			return
		} else {
			deps.Logger.Warn("Unhandled config callback data", zap.String("data", data), zap.Int64("user_id", userID))
			answer.Text = "未知配置操作"
			deps.Bot.Request(answer)
			return // Unknown action
		}
	}

	// If the action requires text input...
	if newStateAction != "" {
		deps.StateManager.SetState(userID, &UserState{
			UserID:    userID,
			ChatID:    chatID,
			MessageID: messageID,
			Action:    newStateAction,
		})
		edit := tgbotapi.NewEditMessageText(chatID, messageID, promptText)
		// Attach the cancel keyboard if defined
		if keyboard != nil {
			edit.ReplyMarkup = keyboard
		} else {
			edit.ReplyMarkup = nil // Ensure no old keyboard remains
		}
		deps.Bot.Send(edit)
		deps.Bot.Request(answer) // Answer the initial callback
		return                   // Waiting for user text input
	}

	// Should not reach here for actions requiring text input or handled above
	deps.StateManager.ClearState(userID) // Clear state if any other action completed implicitly
}

// New helper function to *edit* the config message instead of sending a new one
func HandleMyConfigCommandEdit(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID
	messageID := message.MessageID // Use the provided message ID

	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID)
	var currentSettingsMsg string
	defaultCfg := deps.Config.DefaultGenerationSettings

	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		deps.Logger.Error("Failed to get user config from DB for edit", zap.Error(err), zap.Int64("user_id", userID))
		// Try editing the message to show an error
		edit := tgbotapi.NewEditMessageText(chatID, messageID, "获取您的配置时出错，请稍后再试。")
		deps.Bot.Send(edit)
		return
	}

	imgSize := defaultCfg.ImageSize
	infSteps := defaultCfg.NumInferenceSteps
	guidScale := defaultCfg.GuidanceScale
	numImages := defaultCfg.NumImages // Get default num images

	if userCfg != nil {
		currentSettingsMsg = "您当前的个性化生成设置:"
		if userCfg.ImageSize != nil {
			imgSize = *userCfg.ImageSize
		}
		if userCfg.NumInferenceSteps != nil {
			infSteps = *userCfg.NumInferenceSteps
		}
		if userCfg.GuidanceScale != nil {
			guidScale = *userCfg.GuidanceScale
		}
		if userCfg.NumImages != nil { // Read user's num images if set
			numImages = *userCfg.NumImages
		}
	} else {
		currentSettingsMsg = "您当前使用的是默认生成设置:"
	}

	// Build the settings text using strings.Builder
	var settingsBuilder strings.Builder
	settingsBuilder.WriteString(currentSettingsMsg)
	settingsBuilder.WriteString("\n- 图片尺寸: `")
	settingsBuilder.WriteString(imgSize)
	settingsBuilder.WriteString("`\n- 推理步数: `")
	settingsBuilder.WriteString(strconv.Itoa(infSteps))
	settingsBuilder.WriteString("`\n- Guidance Scale: `")
	settingsBuilder.WriteString(fmt.Sprintf("%.1f`", guidScale))
	settingsBuilder.WriteString("\n- 生成数量: `") // Add num images display
	settingsBuilder.WriteString(strconv.Itoa(numImages))
	settingsBuilder.WriteString("`")
	settingsText := settingsBuilder.String()

	// Create inline keyboard for modification
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置图片尺寸", "config_set_imagesize")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置推理步数", "config_set_infsteps")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置 Guidance Scale", "config_set_guidscale")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置生成数量", "config_set_numimages")), // Add button
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("恢复默认设置", "config_reset_defaults")),
	)

	// Edit the existing message
	edit := tgbotapi.NewEditMessageText(chatID, messageID, settingsText)
	// Switch back to ModeMarkdown
	edit.ParseMode = tgbotapi.ModeMarkdown
	edit.ReplyMarkup = &keyboard
	if _, err := deps.Bot.Send(edit); err != nil {
		deps.Logger.Error("Failed to edit message for /myconfig display", zap.Error(err), zap.Int64("user_id", userID))
	}
}

// Helper to send or edit the Lora selection keyboard
func SendLoraSelectionKeyboard(chatID int64, messageID int, state *UserState, deps BotDeps, edit bool) {
	// Get LoRAs visible to this user
	visibleLoras := GetUserVisibleLoras(state.UserID, deps)

	var rows [][]tgbotapi.InlineKeyboardButton
	maxButtonsPerRow := 2

	// --- Standard Visible LoRAs ---
	// Add Debug log to check state before building buttons
	deps.Logger.Debug("SendLoraSelectionKeyboard: Checking state before adding checkmarks",
		zap.Int64("user_id", state.UserID),
		zap.Strings("selected_loras_in_state", state.SelectedLoras))

	currentRow := []tgbotapi.InlineKeyboardButton{}
	if len(visibleLoras) > 0 {
		for _, lora := range visibleLoras {
			isSelected := false
			for _, selectedName := range state.SelectedLoras {
				if selectedName == lora.Name {
					isSelected = true
					break
				}
			}
			buttonText := lora.Name
			if isSelected {
				buttonText = "✅ " + lora.Name
			}
			// Use Lora ID in callback data for reliable lookup
			button := tgbotapi.NewInlineKeyboardButtonData(buttonText, "lora_select_"+lora.ID)
			currentRow = append(currentRow, button)
			if len(currentRow) == maxButtonsPerRow {
				rows = append(rows, tgbotapi.NewInlineKeyboardRow(currentRow...))
				currentRow = []tgbotapi.InlineKeyboardButton{}
			}
		}
		if len(currentRow) > 0 {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(currentRow...))
			currentRow = []tgbotapi.InlineKeyboardButton{}
		}
	} else {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("无可用 LoRA 风格", "lora_noop")))
	}

	// --- Remove Base LoRA selection from this keyboard ---
	// Base LoRAs are selected in the next step (SendBaseLoraSelectionKeyboard)

	// --- Action Buttons: Done with Standard LoRAs / Cancel ---
	// Show "Next Step" button only if at least one standard LoRA is available
	if len(visibleLoras) > 0 {
		nextButtonText := "➡️ 下一步: 选择 Base LoRA"
		if len(state.SelectedLoras) == 0 {
			// Optional: Disable next step button if none selected? Or rely on callback check.
			// For now, allow clicking, callback handler will check.
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(nextButtonText, "lora_standard_done"), // Corrected callback data
			tgbotapi.NewInlineKeyboardButtonData("❌ 取消", "lora_cancel"),
		))
	} else {
		// Only show Cancel if no LoRAs are available
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("❌ 取消", "lora_cancel"),
		))
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	// Construct the prompt text using strings.Builder, NO MarkdownV2 escaping needed now
	var loraPromptBuilder strings.Builder
	loraPromptBuilder.WriteString("请选择您想使用的标准 LoRA 风格")
	if len(state.SelectedLoras) > 0 {
		// Simple join, backticks should work in ModeMarkdown
		loraPromptBuilder.WriteString(fmt.Sprintf(" (已选: `%s`)", strings.Join(state.SelectedLoras, "`, `")))
	}
	loraPromptBuilder.WriteString(":\nPrompt: ```\n")
	// No need to escape original caption for ModeMarkdown (unless it has _, *, `, [ )
	// Let's assume simple prompts for now. If complex prompts break, add targeted escaping later.
	loraPromptBuilder.WriteString(state.OriginalCaption)
	loraPromptBuilder.WriteString("\n```")
	loraPrompt := loraPromptBuilder.String()

	// Send or Edit the message
	var msg tgbotapi.Chattable
	if edit && messageID != 0 { // Ensure messageID is valid for editing
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, loraPrompt)
		// Switch back to ModeMarkdown
		editMsg.ParseMode = tgbotapi.ModeMarkdown
		editMsg.ReplyMarkup = &keyboard
		msg = editMsg
	} else {
		newMsg := tgbotapi.NewMessage(chatID, loraPrompt)
		// Switch back to ModeMarkdown
		newMsg.ParseMode = tgbotapi.ModeMarkdown
		newMsg.ReplyMarkup = &keyboard
		msg = newMsg
	}

	if _, err := deps.Bot.Send(msg); err != nil {
		deps.Logger.Error("Failed to send/edit Lora selection keyboard", zap.Error(err), zap.Int64("user_id", state.UserID))
	}
}

// GetUserVisibleLoras determines which LoRAs are visible to a specific user based on config.
func GetUserVisibleLoras(userID int64, deps BotDeps) []LoraConfig {
	// Admins see all standard LoRAs defined in the main list
	if deps.Authorizer.IsAdmin(userID) {
		return deps.LoRA
	}

	// If config is nil or sections are missing, return empty (or handle error)
	if deps.Config == nil {
		deps.Logger.Error("Config is nil in GetUserVisibleLoras")
		return []LoraConfig{}
	}

	// 1. Find all groups the user belongs to
	userGroupSet := make(map[string]struct{}) // Use a set for efficient lookup
	for _, group := range deps.Config.UserGroups {
		for _, id := range group.UserIDs {
			if id == userID {
				userGroupSet[group.Name] = struct{}{}
				break // User found in this group, move to next group
			}
		}
	}

	// 2. Filter LoRAs based on AllowGroups
	visibleLoras := []LoraConfig{}
	for _, lora := range deps.LoRA { // Iterate through standard LoRAs
		// Case 1: AllowGroups is empty - LoRA is public to all authorized users
		if len(lora.AllowGroups) == 0 {
			visibleLoras = append(visibleLoras, lora)
			continue // This LoRA is visible, move to the next one
		}

		// Case 2: AllowGroups is not empty - check if user is in any allowed group
		userHasAccess := false
		for _, allowedGroup := range lora.AllowGroups {
			if _, userInGroup := userGroupSet[allowedGroup]; userInGroup {
				userHasAccess = true
				break // User is in one of the allowed groups, grant access
			}
		}

		if userHasAccess {
			visibleLoras = append(visibleLoras, lora)
		}
	}

	// Note: BaseLoRAs are handled separately (e.g., only shown/selectable by admins)
	// If BaseLoRAs should also follow AllowGroups logic, that needs to be integrated here or handled distinctly.

	return visibleLoras
}

// Helper to find LoraConfig by ID (used in callback)
func findLoraByID(loraID string, allLoras []LoraConfig) LoraConfig {
	for _, lora := range allLoras {
		if lora.ID == loraID {
			return lora
		}
	}
	// Also check BaseLoRA if needed, or handle separately
	// for _, lora := range deps.BaseLoRA { ... }
	return LoraConfig{} // Return empty if not found
}

// findLoraByName searches a list of LoraConfig for a LoRA by its name.
// Returns the LoraConfig and a boolean indicating if it was found.
func findLoraByName(name string, loras []LoraConfig) (LoraConfig, bool) {
	for _, lora := range loras {
		if lora.Name == name {
			return lora, true
		}
	}
	return LoraConfig{}, false
}

func GenerateImagesForUser(userState *UserState, deps BotDeps) {
	userID := userState.UserID
	chatID := userState.ChatID
	originalMessageID := userState.MessageID
	deps.StateManager.ClearState(userID) // Clear state early

	if chatID == 0 || originalMessageID == 0 {
		deps.Logger.Error("GenerateImagesForUser called with invalid state", zap.Int64("userID", userID), zap.Int64("chatID", chatID), zap.Int("messageID", originalMessageID))
		deps.Bot.Send(tgbotapi.NewMessage(userID, "❌ 生成失败：内部状态错误，请重试。"))
		return
	}

	// --- Get User/Default Generation Config --- //
	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		deps.Logger.Error("Failed to get user config before generation", zap.Error(err), zap.Int64("user_id", userID))
		// Continue with defaults even if DB fetch fails
	}
	defaultCfg := deps.Config.DefaultGenerationSettings
	prompt := userState.OriginalCaption
	imageSize := defaultCfg.ImageSize
	numInferenceSteps := defaultCfg.NumInferenceSteps
	guidanceScale := defaultCfg.GuidanceScale
	numImages := defaultCfg.NumImages
	if userCfg != nil {
		if userCfg.ImageSize != nil {
			imageSize = *userCfg.ImageSize
		}
		if userCfg.NumInferenceSteps != nil {
			numInferenceSteps = *userCfg.NumInferenceSteps
		}
		if userCfg.GuidanceScale != nil {
			guidanceScale = *userCfg.GuidanceScale
		}
		if userCfg.NumImages != nil {
			numImages = *userCfg.NumImages
		}
	}

	// --- Prepare for Concurrent Requests ---
	if len(userState.SelectedLoras) == 0 {
		deps.Logger.Error("GenerateImagesForUser called with no selected standard LoRAs", zap.Int64("userID", userID))
		edit := tgbotapi.NewEditMessageText(chatID, originalMessageID, "❌ 生成失败：没有选择任何标准 LoRA。")
		deps.Bot.Send(edit)
		return
	}
	numRequests := len(userState.SelectedLoras)

	// Find the selected Base LoRA detail (if any)
	var selectedBaseLoraDetail LoraConfig
	var selectedBaseLoraFound bool
	if userState.SelectedBaseLoraName != "" {
		selectedBaseLoraDetail, selectedBaseLoraFound = findLoraByName(userState.SelectedBaseLoraName, deps.BaseLoRA)
		if !selectedBaseLoraFound {
			deps.Logger.Error("Selected Base LoRA name not found in config, proceeding without it", zap.String("name", userState.SelectedBaseLoraName), zap.Int64("userID", userID))
		} else {
			deps.Logger.Info("Found selected Base LoRA", zap.String("name", selectedBaseLoraDetail.Name), zap.Int64("userID", userID))
		}
	}

	// --- Balance Check (Multiple Requests) ---
	if deps.BalanceManager != nil {
		totalCost := deps.BalanceManager.GetCost() * float64(numRequests)
		currentBal := deps.BalanceManager.GetBalance(userID)
		if currentBal < totalCost {
			errMsg := fmt.Sprintf(errMsgInsufficientBalance+" (需要 %.2f 才能生成 %d 个组合)", deps.BalanceManager.GetCost(), currentBal, totalCost, numRequests)
			deps.Logger.Warn("Insufficient balance for multiple requests", zap.Int64("user_id", userID), zap.Int("num_requests", numRequests), zap.Float64("total_cost", totalCost), zap.Float64("current_balance", currentBal))
			edit := tgbotapi.NewEditMessageText(chatID, originalMessageID, errMsg)
			edit.ReplyMarkup = nil
			deps.Bot.Send(edit)
			return
		} else {
			deps.Logger.Info("User has sufficient balance for multiple requests, deduction will occur per request", zap.Int64("user_id", userID), zap.Int("num_requests", numRequests), zap.Float64("total_cost", totalCost), zap.Float64("current_balance", currentBal))
		}
	}

	// --- Submit Multiple Generation Requests Concurrently ---
	submitTime := time.Now() // Overall start time
	var wg sync.WaitGroup
	// Channel for results: Use a struct to carry more context
	type RequestResult struct {
		Response  *falapi.GenerateResponse
		Error     error
		ReqID     string
		LoraNames []string // LoRAs used for this specific request (Standard + Base if used)
	}
	resultsChan := make(chan RequestResult, numRequests)

	deps.Logger.Info("Starting concurrent generation requests", zap.Int("count", numRequests), zap.String("selected_base_lora", userState.SelectedBaseLoraName))

	// Initial status update
	statusUpdate := fmt.Sprintf("⏳ 正在为 %d 个 LoRA 组合提交生成任务...", numRequests)
	editStatus := tgbotapi.NewEditMessageText(chatID, originalMessageID, statusUpdate)
	deps.Bot.Send(editStatus)

	// Find all standard LoRA details first
	standardLoraDetailsMap := make(map[string]LoraConfig)
	initialErrors := []string{}
	validRequestCount := 0
	for _, name := range userState.SelectedLoras {
		detail, found := findLoraByName(name, deps.LoRA)
		if found {
			standardLoraDetailsMap[name] = detail
			validRequestCount++
		} else {
			deps.Logger.Error("Selected standard LoRA name not found in config during preparation", zap.String("name", name), zap.Int64("userID", userID))
			initialErrors = append(initialErrors, fmt.Sprintf("❌ 内部错误：找不到标准 LoRA '%s' 的配置", name))
			// Don't launch a goroutine for this one
		}
	}
	// Adjust numRequests if some were invalid upfront
	if validRequestCount < numRequests {
		deps.Logger.Warn("Some selected standard LoRAs were invalid, reducing request count", zap.Int("original_count", numRequests), zap.Int("valid_count", validRequestCount))
		numRequests = validRequestCount
	}

	// Launch goroutines only for valid standard LoRAs
	for _, standardLora := range standardLoraDetailsMap {
		wg.Add(1)
		go func(sl LoraConfig) { // Pass standardLora by value
			defer wg.Done()
			// Result struct for this specific request
			requestResult := RequestResult{LoraNames: []string{sl.Name}} // Start with standard LoRA name

			// --- Individual Balance Deduction ---
			if deps.BalanceManager != nil {
				canProceed, deductErr := deps.BalanceManager.CheckAndDeduct(userID)
				if !canProceed {
					errMsg := fmt.Sprintf("❌ 扣费失败 (LoRA: %s)", sl.Name)
					if deductErr != nil {
						errMsg = fmt.Sprintf("❌ 扣费失败 (LoRA: %s): %s", sl.Name, deductErr.Error())
					}
					deps.Logger.Warn("Individual balance deduction failed", zap.Int64("user_id", userID), zap.String("lora", sl.Name), zap.Error(deductErr))
					requestResult.Error = fmt.Errorf(errMsg)
					resultsChan <- requestResult
					return
				}
				deps.Logger.Info("Balance deducted for LoRA request", zap.Int64("user_id", userID), zap.String("lora", sl.Name))
			}

			// --- Prepare LoRAs for this specific request (Max 2) ---
			lorasForThisRequest := []falapi.LoraWeight{}
			addedURLsForThisRequest := make(map[string]struct{})

			// Add the standard LoRA
			lorasForThisRequest = append(lorasForThisRequest, falapi.LoraWeight{Path: sl.URL, Scale: sl.Weight})
			addedURLsForThisRequest[sl.URL] = struct{}{}

			// Add the selected Base LoRA if found, different URL, and space allows (max 2 total)
			if selectedBaseLoraFound && len(lorasForThisRequest) < 2 {
				if _, exists := addedURLsForThisRequest[selectedBaseLoraDetail.URL]; !exists {
					lorasForThisRequest = append(lorasForThisRequest, falapi.LoraWeight{Path: selectedBaseLoraDetail.URL, Scale: selectedBaseLoraDetail.Weight})
					requestResult.LoraNames = append(requestResult.LoraNames, selectedBaseLoraDetail.Name) // Add base name
					deps.Logger.Debug("Adding selected Base LoRA to request", zap.String("base_lora", selectedBaseLoraDetail.Name), zap.String("standard_lora", sl.Name))
				} else {
					deps.Logger.Debug("Skipping adding Base LoRA as its URL is same as standard LoRA", zap.String("base_lora", selectedBaseLoraDetail.Name), zap.String("standard_lora", sl.Name))
					// Still add base lora name for clarity in results if needed?
					// Let's add it for the result context even if not sent to API due to duplicate URL
					baseNameAlreadyInList := false
					for _, n := range requestResult.LoraNames {
						if n == selectedBaseLoraDetail.Name {
							baseNameAlreadyInList = true
							break
						}
					}
					if !baseNameAlreadyInList {
						requestResult.LoraNames = append(requestResult.LoraNames, selectedBaseLoraDetail.Name)
					}
				}
			} else if selectedBaseLoraFound && len(lorasForThisRequest) >= 2 {
				deps.Logger.Debug("Skipping adding Base LoRA as request already has max LoRAs", zap.String("base_lora", selectedBaseLoraDetail.Name), zap.String("standard_lora", sl.Name))
				// Add base lora name for clarity in results
				baseNameAlreadyInList := false
				for _, n := range requestResult.LoraNames {
					if n == selectedBaseLoraDetail.Name {
						baseNameAlreadyInList = true
						break
					}
				}
				if !baseNameAlreadyInList {
					requestResult.LoraNames = append(requestResult.LoraNames, selectedBaseLoraDetail.Name)
				}
			}

			// --- Submit Single Request ---
			deps.Logger.Debug("Submitting request for LoRA combo", zap.Strings("names", requestResult.LoraNames), zap.Int("api_lora_count", len(lorasForThisRequest)))
			requestID, err := deps.FalClient.SubmitGenerationRequest(
				prompt,
				lorasForThisRequest,     // Final list (1 or 2 items)
				requestResult.LoraNames, // Names for logging/context
				fmt.Sprintf("%v", imageSize),
				numInferenceSteps,
				guidanceScale,
				numImages,
			)
			if err != nil {
				errMsg := fmt.Sprintf("❌ 提交失败 (%s): %s", strings.Join(requestResult.LoraNames, "+"), err.Error())
				deps.Logger.Error("SubmitGenerationRequest failed", zap.Error(err), zap.Int64("user_id", userID), zap.Strings("loras", requestResult.LoraNames))
				requestResult.Error = fmt.Errorf(errMsg)
				if deps.BalanceManager != nil {
					deps.Logger.Warn("Submission failed after deduction, no refund method.", zap.Int64("user_id", userID), zap.Strings("loras", requestResult.LoraNames), zap.Float64("amount", deps.BalanceManager.GetCost()))
				}
				resultsChan <- requestResult
				return
			}
			requestResult.ReqID = requestID // Store request ID
			deps.Logger.Info("Submitted individual task", zap.Int64("user_id", userID), zap.String("request_id", requestID), zap.Strings("loras", requestResult.LoraNames))

			// --- Poll For Result ---
			pollInterval := 5 * time.Second
			generationTimeout := 5 * time.Minute
			ctx, cancel := context.WithTimeout(context.Background(), generationTimeout)
			defer cancel()

			result, err := deps.FalClient.PollForResult(ctx, requestID, deps.Config.APIEndpoints.FluxLora, pollInterval)
			if err != nil {
				// Try to make error message more user-friendly
				errMsg := ""
				rawErrMsg := err.Error()
				if errors.Is(err, context.DeadlineExceeded) {
					errMsg = fmt.Sprintf("❌ 获取结果超时 (%s, ID: ...%s)", strings.Join(requestResult.LoraNames, "+"), truncateID(requestID))
				} else if strings.Contains(rawErrMsg, "API status check failed with status 422") || strings.Contains(rawErrMsg, "API result fetch failed with status 422") {
					errMsg = fmt.Sprintf("❌ API 错误 (%s): 422 - 无效组合?", strings.Join(requestResult.LoraNames, "+"))
					// Attempt to extract more detail
					if idx := strings.Index(rawErrMsg, "{\"detail\":"); idx != -1 {
						var detail struct {
							Detail []struct {
								Msg string `json:"msg"`
							} `json:"detail"`
						}
						if json.Unmarshal([]byte(rawErrMsg[idx:]), &detail) == nil && len(detail.Detail) > 0 {
							errMsg += " (" + detail.Detail[0].Msg + ")"
						}
					}
				} else {
					errMsg = fmt.Sprintf("❌ 获取结果失败 (%s, ID: ...%s): %s", strings.Join(requestResult.LoraNames, "+"), truncateID(requestID), rawErrMsg)
				}

				deps.Logger.Error("PollForResult failed", zap.Error(err), zap.Int64("user_id", userID), zap.String("request_id", requestID), zap.Strings("loras", requestResult.LoraNames))
				requestResult.Error = fmt.Errorf(errMsg) // Use formatted error
				resultsChan <- requestResult
				return
			}

			deps.Logger.Info("Successfully polled result", zap.String("request_id", requestID), zap.Strings("loras", requestResult.LoraNames))
			requestResult.Response = result
			resultsChan <- requestResult // Send successful result

		}(standardLora) // Pass LoraConfig by value
	}

	// Goroutine to close channel once all workers are done
	go func() {
		wg.Wait()
		close(resultsChan)
		deps.Logger.Info("All generation goroutines finished.")
	}()

	// --- Collect Results ---
	var successfulResults []RequestResult
	var errorsCollected []RequestResult
	numCompleted := 0

	// Append initial errors (e.g., LoRA not found in config)
	for _, errMsg := range initialErrors {
		errorsCollected = append(errorsCollected, RequestResult{Error: fmt.Errorf(errMsg)})
	}

	deps.Logger.Info("Waiting for generation results...")
	for res := range resultsChan {
		numCompleted++
		// Update status periodically
		// Use validRequestCount here because initial errors don't reach the channel this way
		statusUpdate := fmt.Sprintf("⏳ %d / %d 个 LoRA 组合完成...", numCompleted, validRequestCount)
		editStatus := tgbotapi.NewEditMessageText(chatID, originalMessageID, statusUpdate)
		deps.Bot.Send(editStatus)

		if res.Error != nil {
			errorsCollected = append(errorsCollected, res)
			deps.Logger.Warn("Collected error result", zap.Strings("loras", res.LoraNames), zap.String("reqID", res.ReqID), zap.Error(res.Error))
		} else if res.Response != nil {
			successfulResults = append(successfulResults, res)
			deps.Logger.Info("Collected successful result", zap.Strings("loras", res.LoraNames), zap.String("reqID", res.ReqID), zap.Int("image_count", len(res.Response.Images)))
		} else {
			// Should not happen
			deps.Logger.Error("Collected result with nil Response and nil Error", zap.Strings("loras", res.LoraNames), zap.String("reqID", res.ReqID))
			errorsCollected = append(errorsCollected, RequestResult{Error: fmt.Errorf("内部错误：收到空结果 (LoRA: %s)", strings.Join(res.LoraNames, ","))})
		}
	}

	// --- Process Collected Results ---
	duration := time.Since(submitTime) // Total duration
	deps.Logger.Info("Finished collecting results", zap.Int("success_count", len(successfulResults)), zap.Int("error_count", len(errorsCollected)), zap.Duration("total_duration", duration))

	// Combine all images from successful results
	allImages := []falapi.ImageInfo{}
	for _, result := range successfulResults {
		if result.Response != nil {
			allImages = append(allImages, result.Response.Images...)
		}
	}

	// --- Handle Final Outcome ---
	finalCaption := ""
	if len(allImages) > 0 {
		// Success case (at least one image generated)
		deps.Logger.Info("Generation finished with images", zap.Int64("user_id", userID), zap.Int("total_images", len(allImages)), zap.Int("successful_requests", len(successfulResults)), zap.Int("failed_requests", len(errorsCollected)))

		// Build caption
		captionBuilder := strings.Builder{}
		captionBuilder.WriteString(fmt.Sprintf("📝 Prompt: ```\n%s\n```\n---\n", userState.OriginalCaption))

		if len(successfulResults) > 0 {
			captionBuilder.WriteString(fmt.Sprintf("✅ %d 个组合成功: ", len(successfulResults)))
			var successNames []string
			for _, r := range successfulResults {
				// Ensure LoraNames is not empty before joining
				if len(r.LoraNames) > 0 {
					successNames = append(successNames, fmt.Sprintf("`%s`", strings.Join(r.LoraNames, "+")))
				} else {
					successNames = append(successNames, "`(未知组合)`") // Fallback if names were missing
				}
			}
			captionBuilder.WriteString(strings.Join(successNames, ", "))
			captionBuilder.WriteString("\n")
		}

		if len(errorsCollected) > 0 {
			captionBuilder.WriteString(fmt.Sprintf("⚠️ %d 个组合失败/跳过: ", len(errorsCollected)))
			var errorSummaries []string
			for _, e := range errorsCollected {
				// Simplified error summary for debugging linter error
				if e.Error != nil {
					errorSummaries = append(errorSummaries, e.Error.Error())
				} else {
					errorSummaries = append(errorSummaries, "(未知错误)")
				}
			}
			captionBuilder.WriteString(strings.Join(errorSummaries, ", "))
			captionBuilder.WriteString("\n")
		}

		captionBuilder.WriteString(fmt.Sprintf("⏱️ 总耗时: %.1fs", duration.Seconds()))

		if deps.BalanceManager != nil {
			finalBalance := deps.BalanceManager.GetBalance(userID)
			captionBuilder.WriteString(fmt.Sprintf("\n💰 余额: %.2f", finalBalance))
		}
		finalCaption = captionBuilder.String()

		// --- Send Results ---
		var sendErr error
		if len(allImages) == 1 {
			// Send single photo
			img := allImages[0]
			photoMsg := tgbotapi.NewPhoto(chatID, tgbotapi.FileURL(img.URL))
			photoMsg.Caption = finalCaption
			// Switch back to ModeMarkdown
			photoMsg.ParseMode = tgbotapi.ModeMarkdown
			if _, err := deps.Bot.Send(photoMsg); err != nil {
				deps.Logger.Error("Failed to send single combined photo", zap.Error(err), zap.Int64("user_id", userID))
				sendErr = err
			}
		} else {
			// Send multiple photos as Media Group(s)
			var mediaGroup []interface{}
			// Send caption as separate message BEFORE media group
			captionMsg := tgbotapi.NewMessage(chatID, finalCaption)
			// Switch back to ModeMarkdown
			captionMsg.ParseMode = tgbotapi.ModeMarkdown
			if _, err := deps.Bot.Send(captionMsg); err != nil {
				deps.Logger.Error("Failed to send caption before media group", zap.Error(err), zap.Int64("user_id", userID))
				// Continue trying to send images
			}

			for _, img := range allImages {
				photo := tgbotapi.NewInputMediaPhoto(tgbotapi.FileURL(img.URL))
				mediaGroup = append(mediaGroup, photo)
				if len(mediaGroup) == 10 {
					mediaMessage := tgbotapi.NewMediaGroup(chatID, mediaGroup)
					if _, err := deps.Bot.Request(mediaMessage); err != nil {
						deps.Logger.Error("Failed to send image group chunk", zap.Error(err), zap.Int64("user_id", userID), zap.Int("chunk_size", len(mediaGroup)))
						if sendErr == nil {
							sendErr = err
						}
					}
					mediaGroup = []interface{}{}
				}
			}
			if len(mediaGroup) > 0 {
				mediaMessage := tgbotapi.NewMediaGroup(chatID, mediaGroup)
				if _, err := deps.Bot.Request(mediaMessage); err != nil {
					deps.Logger.Error("Failed to send final image group", zap.Error(err), zap.Int64("user_id", userID), zap.Int("group_size", len(mediaGroup)))
					if sendErr == nil {
						sendErr = err
					}
				}
			}
		}
		// --- End Send Results ---

		// Delete original status message ONLY if sending was successful
		if sendErr == nil {
			deleteMsg := tgbotapi.NewDeleteMessage(chatID, originalMessageID)
			if _, errDel := deps.Bot.Request(deleteMsg); errDel != nil {
				deps.Logger.Warn("Failed to delete original status message after sending results", zap.Error(errDel), zap.Int64("user_id", userID), zap.Int("message_id", originalMessageID))
			}
		} else {
			// Edit original message to show send error AND generation summary
			failedSendText := fmt.Sprintf("✅ %d 张图片生成完成, 但发送图片失败: %s\n\n%s", len(allImages), sendErr.Error(), finalCaption) // Include original caption info
			// Ensure length constraints
			if len(failedSendText) > 4090 {
				failedSendText = failedSendText[:4090] + "..."
			}
			editErr := tgbotapi.NewEditMessageText(chatID, originalMessageID, failedSendText)
			// Switch back to ModeMarkdown
			editErr.ParseMode = tgbotapi.ModeMarkdown
			editErr.ReplyMarkup = nil
			deps.Bot.Send(editErr)
		}

	} else {
		// Failure case (no images generated at all)
		deps.Logger.Error("Generation finished with no images", zap.Int64("user_id", userID), zap.Int("failed_requests", len(errorsCollected)))
		errMsg := "❌ 所有 LoRA 组合生成失败。"
		if len(errorsCollected) > 0 {
			errMsg += "\n\n失败详情:"
			for _, e := range errorsCollected {
				if e.Error != nil { // Check if error exists
					errMsg += fmt.Sprintf("\n- %s", e.Error.Error()) // Show full error message here
				}
			}
		}
		// Truncate error message if too long
		if len(errMsg) > 4090 {
			errMsg = errMsg[:4090] + "..."
		}

		// Correctly add balance to the original errMsg for complete failure scenario
		if deps.BalanceManager != nil {
			finalBalance := deps.BalanceManager.GetBalance(userID)
			errMsg += fmt.Sprintf("\n\n💰 余额: %.2f", finalBalance)
		}

		edit := tgbotapi.NewEditMessageText(chatID, originalMessageID, errMsg)
		// Switch back to ModeMarkdown
		edit.ParseMode = tgbotapi.ModeMarkdown
		edit.ReplyMarkup = nil
		deps.Bot.Send(edit)
	}

}

// Helper to get user groups (can be moved to a more suitable place like auth or utils)
func GetUserGroups(userID int64, deps BotDeps) map[string]struct{} {
	userGroupSet := make(map[string]struct{})
	if deps.Config == nil || deps.Config.UserGroups == nil {
		return userGroupSet // Return empty set if config is missing
	}
	for _, group := range deps.Config.UserGroups {
		for _, id := range group.UserIDs {
			if id == userID {
				userGroupSet[group.Name] = struct{}{}
				break
			}
		}
	}
	return userGroupSet
}

// Helper to truncate long request IDs for display
func truncateID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

// Handles the /myconfig command
func HandleMyConfigCommand(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID

	// Fetch user's config from DB
	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID) // Use aliased package

	var currentSettingsMsg string
	defaultCfg := deps.Config.DefaultGenerationSettings

	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		deps.Logger.Error("Failed to get user config from DB", zap.Error(err), zap.Int64("user_id", userID))
		deps.Bot.Send(tgbotapi.NewMessage(chatID, "获取您的配置时出错，请稍后再试。"))
		return
	}

	// Determine current settings to display
	imgSize := defaultCfg.ImageSize
	infSteps := defaultCfg.NumInferenceSteps
	guidScale := defaultCfg.GuidanceScale
	numImages := defaultCfg.NumImages // Get default num images

	if userCfg != nil { // User has custom config
		currentSettingsMsg = "您当前的个性化生成设置:"
		if userCfg.ImageSize != nil {
			imgSize = *userCfg.ImageSize
		}
		if userCfg.NumInferenceSteps != nil {
			infSteps = *userCfg.NumInferenceSteps
		}
		if userCfg.GuidanceScale != nil {
			guidScale = *userCfg.GuidanceScale
		}
		if userCfg.NumImages != nil { // Read user's num images if set
			numImages = *userCfg.NumImages
		}
	} else {
		currentSettingsMsg = "您当前使用的是默认生成设置:"
	}

	// Build the settings text using strings.Builder
	var settingsBuilder strings.Builder
	settingsBuilder.WriteString(currentSettingsMsg)
	settingsBuilder.WriteString("\n- 图片尺寸: `")
	settingsBuilder.WriteString(imgSize)
	settingsBuilder.WriteString("`\n- 推理步数: `")
	settingsBuilder.WriteString(strconv.Itoa(infSteps))
	settingsBuilder.WriteString("`\n- Guidance Scale: `")
	settingsBuilder.WriteString(fmt.Sprintf("%.1f`", guidScale))
	settingsBuilder.WriteString("\n- 生成数量: `") // Add num images display
	settingsBuilder.WriteString(strconv.Itoa(numImages))
	settingsBuilder.WriteString("`")
	settingsText := settingsBuilder.String()

	// Create inline keyboard for modification
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置图片尺寸", "config_set_imagesize")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置推理步数", "config_set_infsteps")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置 Guidance Scale", "config_set_guidscale")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置生成数量", "config_set_numimages")), // Add button
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("恢复默认设置", "config_reset_defaults")),
	)

	reply := tgbotapi.NewMessage(chatID, settingsText)
	// Switch back to ModeMarkdown
	reply.ParseMode = tgbotapi.ModeMarkdown
	reply.ReplyMarkup = keyboard
	deps.Bot.Send(reply)
}

// Handles text input when user is expected to provide a config value
func HandleConfigUpdateInput(message *tgbotapi.Message, state *UserState, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID
	inputText := message.Text

	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		sendGenericError(chatID, userID, "GetConfigForUpdate", err, deps)
		deps.StateManager.ClearState(userID) // Clear state on error
		return
	}
	if userCfg == nil {
		userCfg = &st.UserGenerationConfig{UserID: userID} // Initialize if not found
	}

	var updateErr error
	action := state.Action // e.g., "awaiting_config_infsteps"

	switch action {
	case "awaiting_config_infsteps":
		steps, err := strconv.Atoi(inputText)
		if err != nil || steps <= 0 || steps > 50 {
			// More specific error, ask user to retry
			deps.Bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("%s 请输入 1 到 50 之间的整数。", errMsgInvalidConfigInput)))
			return // Don't clear state, let user try again
		}
		userCfg.NumInferenceSteps = &steps
		updateErr = st.SetUserGenerationConfig(deps.DB, userID, *userCfg)

	case "awaiting_config_guidscale":
		scale, err := strconv.ParseFloat(inputText, 64)
		if err != nil || scale <= 0 || scale > 15 {
			// More specific error, ask user to retry
			deps.Bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("%s 请输入 0 到 15 之间的数字 (例如 7.0)。", errMsgInvalidConfigInput)))
			return // Don't clear state
		}
		userCfg.GuidanceScale = &scale
		updateErr = st.SetUserGenerationConfig(deps.DB, userID, *userCfg)

	case "awaiting_config_numimages":
		numImages, err := strconv.Atoi(inputText)
		// Validate the input (e.g., 1-10, adjust as needed)
		if err != nil || numImages <= 0 || numImages > 10 {
			deps.Bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("%s 请输入 1 到 10 之间的整数。", errMsgInvalidConfigInput)))
			return // Don't clear state, let user try again
		}
		userCfg.NumImages = &numImages
		updateErr = st.SetUserGenerationConfig(deps.DB, userID, *userCfg)

	default:
		deps.Logger.Warn("Received text input in unexpected config state", zap.String("action", action), zap.Int64("user_id", userID))
		deps.Bot.Send(tgbotapi.NewMessage(chatID, "内部错误：未知的配置状态。"))
	}

	if updateErr != nil {
		sendGenericError(chatID, userID, "SetConfigValue", updateErr, deps)
	} else {
		deps.Logger.Info("User config updated successfully", zap.Int64("user_id", userID), zap.String("action", action))
		deps.Bot.Send(tgbotapi.NewMessage(chatID, "✅ 配置已更新！"))
		// Send a new message showing the updated config
		syntheticMsg := &tgbotapi.Message{
			From: message.From, // Use current message context
			Chat: message.Chat,
		}
		HandleMyConfigCommand(syntheticMsg, deps) // Call the function that SENDS the config message
	}
	deps.StateManager.ClearState(userID) // Clear state after successful update or unrecoverable error
}

// HandleHelpCommand sends the help message.
func HandleHelpCommand(chatID int64, deps BotDeps) {
	// Adjusted help text for ModeMarkdown (escape * and `)
	helpText := `
*欢迎使用 Flux LoRA 图片生成 Bot*！ 🎨

你可以通过以下方式使用我：

1\.  \*发送图片\*：我会自动描述这张图片，然后你可以确认或修改描述，并选择 LoRA 风格来生成新的图片。
2\.  \*直接发送文本描述\*：我会直接使用你的文本作为提示词 \(Prompt\)，让你选择 LoRA 风格并生成图片。

*可用命令*:
/start \- 显示欢迎信息
/help \- 显示此帮助信息
/loras \- 查看你当前可用的 LoRA 风格列表
/myconfig \- 查看和修改你的个性化图片生成参数（尺寸、步数等）
/balance \- 查询你当前的生成点数余额 \(如果启用了此功能\)
/version \- 查看当前 Bot 的版本信息

*生成流程*:
\- 发送图片或文本后，我会提示你选择 LoRA 风格。
\- 点击 LoRA 名称按钮进行选择/取消选择。
\- 选择完毕后，点击\"下一步\"或\"生成图片\"按钮。
\- 生成过程可能需要一些时间，请耐心等待。

*提示*:
\- 高质量、清晰的描述有助于生成更好的图片。
\- 尝试不同的 LoRA 风格组合！

祝你使用愉快！✨
`
	reply := tgbotapi.NewMessage(chatID, helpText)
	// Switch back to ModeMarkdown
	reply.ParseMode = tgbotapi.ModeMarkdown
	deps.Bot.Send(reply)
}

// SendBaseLoraSelectionKeyboard sends or edits the message for selecting a single Base LoRA.
func SendBaseLoraSelectionKeyboard(chatID int64, messageID int, state *UserState, deps BotDeps, edit bool) {
	// Determine visible Base LoRAs (e.g., only for admins, or based on groups)
	visibleBaseLoras := []LoraConfig{}
	if deps.Authorizer.IsAdmin(state.UserID) {
		visibleBaseLoras = deps.BaseLoRA // Admins can select from all base LoRAs
		deps.Logger.Debug("Admin user, showing all base LoRAs for selection", zap.Int64("user_id", state.UserID), zap.Int("count", len(visibleBaseLoras)))
	} else {
		deps.Logger.Debug("Non-admin user, not showing base LoRAs for explicit selection", zap.Int64("user_id", state.UserID))
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	maxButtonsPerRow := 2
	promptBuilder := strings.Builder{}

	// Build prompt text, no need for complex escaping
	promptBuilder.WriteString(fmt.Sprintf("已选标准 LoRA: `%s`\n", strings.Join(state.SelectedLoras, "`, `")))
	promptBuilder.WriteString("请选择 **最多一个** Base LoRA (可选):\n") // No escaping needed for parens in ModeMarkdown
	if state.SelectedBaseLoraName != "" {
		// Use backticks for name, should be fine
		promptBuilder.WriteString(fmt.Sprintf("\n当前 Base LoRA: `%s`", state.SelectedBaseLoraName))
	}

	// --- Base LoRA Buttons ---
	currentRow := []tgbotapi.InlineKeyboardButton{}
	if len(visibleBaseLoras) > 0 {
		for _, lora := range visibleBaseLoras {
			buttonText := lora.Name
			if state.SelectedBaseLoraName == lora.Name {
				buttonText = "✅ " + lora.Name // Mark selected
			}
			button := tgbotapi.NewInlineKeyboardButtonData(buttonText, "base_lora_select_"+lora.ID)
			currentRow = append(currentRow, button)
			if len(currentRow) == maxButtonsPerRow {
				rows = append(rows, tgbotapi.NewInlineKeyboardRow(currentRow...))
				currentRow = []tgbotapi.InlineKeyboardButton{}
			}
		}
		if len(currentRow) > 0 { // Add remaining buttons
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(currentRow...))
		}
	} else {
		// No base loras available/visible for selection
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("(无可用 Base LoRA)", "lora_noop"))) // No escaping needed
	}

	// --- Action Buttons ---
	skipButtonText := "➡️ 跳过 Base LoRA"
	if state.SelectedBaseLoraName == "" {
		skipButtonText = "➡️ (已跳过)" // No escaping needed
	} else {
		skipButtonText = "❌ 取消选择 Base LoRA"
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(skipButtonText, "base_lora_skip"), // Skip/Deselect
	))
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ 确认生成", "lora_confirm_generate"),
		tgbotapi.NewInlineKeyboardButtonData("🚫 取消", "base_lora_cancel"),
	))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
	finalPrompt := promptBuilder.String()

	// Send or Edit the message
	var msg tgbotapi.Chattable
	if edit && messageID != 0 {
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, finalPrompt)
		// Switch back to ModeMarkdown
		editMsg.ParseMode = tgbotapi.ModeMarkdown
		editMsg.ReplyMarkup = &keyboard
		msg = editMsg
	} else {
		newMsg := tgbotapi.NewMessage(chatID, finalPrompt)
		// Switch back to ModeMarkdown
		newMsg.ParseMode = tgbotapi.ModeMarkdown
		newMsg.ReplyMarkup = &keyboard
		msg = newMsg
	}

	if _, err := deps.Bot.Send(msg); err != nil {
		deps.Logger.Error("Failed to send/edit Base LoRA selection keyboard", zap.Error(err), zap.Int64("user_id", state.UserID))
	}
}
