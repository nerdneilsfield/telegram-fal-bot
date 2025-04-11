package bot

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	// Use context for potentially long running operations

	st "github.com/nerdneilsfield/telegram-fal-bot/internal/storage"
	"github.com/nerdneilsfield/telegram-fal-bot/pkg/falapi"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

	// 清理可能过期的状态
	deps.StateManager.ClearState(userID)

	// 命令处理
	if message.IsCommand() {
		switch message.Command() {
		case "start":
			reply := tgbotapi.NewMessage(chatID, "欢迎使用 Flux LoRA 图片生成 Bot！\n发送图片进行描述和生成，或直接发送描述文本生成图片。\n使用 /balance 查看余额。\n使用 /loras 查看可用风格。\n使用 /myconfig 查看或修改您的生成参数。\n使用 /version 查看版本信息。")
			reply.ParseMode = tgbotapi.ModeMarkdown
			deps.Bot.Send(reply)
		case "help": // Handle /help command
			HandleHelpCommand(chatID, deps)
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
					loraList.WriteString(fmt.Sprintf("- %s\n", lora.Name))
				}
			} else {
				loraList.WriteString("当前没有可用的 LoRA 风格。")
			}

			// Admins can also see BaseLoRAs
			if deps.Authorizer.IsAdmin(userID) && len(deps.BaseLoRA) > 0 {
				loraList.WriteString("\nBase LoRA 风格 (仅管理员可见):\n")
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

		case "myconfig":
			HandleMyConfigCommand(message, deps)

		case "set":
			if !deps.Authorizer.IsAdmin(userID) {
				deps.Bot.Send(tgbotapi.NewMessage(chatID, "只有管理员才能使用此命令。"))
				return
			}
			deps.Bot.Send(tgbotapi.NewMessage(chatID, "管理员设置功能正在开发中..."))

		default:
			deps.Bot.Send(tgbotapi.NewMessage(chatID, "未知命令。"))
		}
		return
	}

	// 图片消息处理
	if message.Photo != nil && len(message.Photo) > 0 {
		HandlePhotoMessage(message, deps)
		return
	}

	// 文本消息处理 (Prompt or potentially config update)
	if message.Text != "" {
		state, exists := deps.StateManager.GetState(userID)
		if exists && strings.HasPrefix(state.Action, "awaiting_config_") {
			HandleConfigUpdateInput(message, state, deps)
		} else {
			HandleTextMessage(message, deps)
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
			editMsg.ParseMode = tgbotapi.ModeMarkdown
			editMsg.ReplyMarkup = &confirmationKeyboard
			finalMsg = editMsg
		} else {
			newMsg := tgbotapi.NewMessage(originalChatID, msgText)
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
		SendLoraSelectionKeyboard(chatID, msgIDForKeyboard, newState, deps, true)
	} else {
		// Fallback if sending waitMsg failed? Maybe send a new message with keyboard.
		deps.Logger.Warn("Could not send wait message, sending keyboard as new message", zap.Int64("user_id", userID))
		SendLoraSelectionKeyboard(chatID, 0, newState, deps, false) // Send as new message
	}
}

func HandleCallbackQuery(callbackQuery *tgbotapi.CallbackQuery, deps BotDeps) {
	userID := callbackQuery.From.ID
	// Ensure Chat is not nil before accessing ID
	var chatID int64
	var messageID int
	if callbackQuery.Message != nil {
		chatID = callbackQuery.Message.Chat.ID
		messageID = callbackQuery.Message.MessageID
	} else {
		deps.Logger.Error("Callback query message is nil", zap.Int64("user_id", userID), zap.String("data", callbackQuery.Data))
		// Answer the callback to prevent infinite loading, but can't do much else
		answer := tgbotapi.NewCallback(callbackQuery.ID, "错误：无法处理此操作。") // Improved feedback
		deps.Bot.Request(answer)
		return
	}
	data := callbackQuery.Data

	deps.Logger.Info("Callback received", zap.Int64("user_id", userID), zap.String("data", data), zap.Int64("chat_id", chatID), zap.Int("message_id", messageID))

	answer := tgbotapi.NewCallback(callbackQuery.ID, "") // Prepare default answer

	// --- Configuration Callbacks ---
	if strings.HasPrefix(data, "config_") {
		HandleConfigCallback(callbackQuery, deps) // Pass the full callbackQuery
		return
	}

	// --- Existing Callbacks ---
	state, ok := deps.StateManager.GetState(userID)
	if !ok {
		deps.Logger.Warn("Received callback but no state found or state expired", zap.Int64("user_id", userID), zap.String("data", data))
		answer.Text = errMsgStateExpired // Use constant
		deps.Bot.Request(answer)
		edit := tgbotapi.NewEditMessageText(chatID, messageID, errMsgStateExpired)
		edit.ReplyMarkup = nil
		deps.Bot.Send(edit)
		return
	}

	// Ensure state has chat/message ID (should be set when state is created)
	if state.ChatID == 0 {
		state.ChatID = chatID
	}
	if state.MessageID == 0 {
		state.MessageID = messageID
	}

	switch state.Action {
	case "awaiting_caption_confirmation":
		switch data {
		case "caption_confirm":
			answer.Text = "开始选择 LoRA 风格..."
			deps.Bot.Request(answer)
			state.Action = "awaiting_lora_selection"
			// MessageID for keyboard is already in state
			deps.StateManager.SetState(userID, state)
			SendLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true) // Edit existing message
		case "caption_cancel":
			answer.Text = "操作已取消"
			deps.Bot.Request(answer)
			deps.StateManager.ClearState(userID)
			edit := tgbotapi.NewEditMessageText(chatID, messageID, "操作已取消。")
			edit.ReplyMarkup = nil // Clear keyboard
			deps.Bot.Send(edit)
		default:
			answer.Text = "未知操作"
			deps.Bot.Request(answer)
		}

	case "awaiting_lora_selection":
		if strings.HasPrefix(data, "lora_select_") {
			loraID := strings.TrimPrefix(data, "lora_select_")
			// Use combined list for lookup by ID
			allLoras := append(deps.LoRA, deps.BaseLoRA...)
			selectedLora := findLoraByID(loraID, allLoras)

			if selectedLora.ID == "" { // Not found
				answer.Text = "错误：无效的 LoRA 选择"
				deps.Bot.Request(answer)
				deps.Logger.Warn("Invalid lora ID selected", zap.String("loraID", loraID), zap.Int64("user_id", userID))
				return
			}

			// Toggle selection using Lora Name in state
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
			deps.StateManager.SetState(userID, state)

			// Update keyboard
			ansText := fmt.Sprintf("已选: %s", strings.Join(state.SelectedLoras, ", "))
			if len(state.SelectedLoras) == 0 {
				ansText = "请选择至少一个 LoRA"
			}
			answer.Text = ansText
			deps.Bot.Request(answer)
			SendLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true)

		} else if data == "lora_confirm" {
			if len(state.SelectedLoras) == 0 {
				answer.Text = "请至少选择一个 LoRA 风格！"
				deps.Bot.Request(answer)
				return
			}
			answer.Text = "正在提交生成请求..."
			deps.Bot.Request(answer)

			// Edit the message identified in state.MessageID
			editText := fmt.Sprintf("⏳ 正在使用 LoRAs: `%s` 生成图片...\nPrompt: ```\n%s\n```",
				strings.Join(state.SelectedLoras, ", "), state.OriginalCaption)
			edit := tgbotapi.NewEditMessageText(state.ChatID, state.MessageID, editText)
			edit.ParseMode = tgbotapi.ModeMarkdown
			edit.ReplyMarkup = nil // Clear keyboard
			deps.Bot.Send(edit)

			go GenerateImagesForUser(state, deps)
			// State cleared within the goroutine

		} else if data == "lora_cancel" {
			answer.Text = "操作已取消"
			deps.Bot.Request(answer)
			deps.StateManager.ClearState(userID)
			edit := tgbotapi.NewEditMessageText(chatID, messageID, "操作已取消。")
			edit.ReplyMarkup = nil // Clear keyboard
			deps.Bot.Send(edit)
		} else if data == "lora_noop" {
			// Do nothing, just answer the callback
			deps.Bot.Request(answer)
		} else {
			answer.Text = "未知 LoRA 操作"
			deps.Bot.Request(answer)
		}

	default:
		// ... (existing default handling) ...
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
		keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
		edit := tgbotapi.NewEditMessageText(chatID, messageID, "请选择新的图片尺寸:") // Update text as well
		edit.ReplyMarkup = &keyboard
		deps.Bot.Send(edit)
		return // Waiting for selection

	case "config_set_infsteps":
		answer.Text = "请输入推理步数 (1-50)"
		newStateAction = "awaiting_config_infsteps"
		promptText = "请输入您想要的推理步数 (1-50):"

	case "config_set_guidscale":
		answer.Text = "请输入 Guidance Scale (0-15)"
		newStateAction = "awaiting_config_guidscale"
		promptText = "请输入您想要的 Guidance Scale (例如: 7.0):"

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
			HandleMyConfigCommandEdit(syntheticMsg, deps)
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
		HandleMyConfigCommandEdit(syntheticMsg, deps)
		deps.StateManager.ClearState(userID)
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
				HandleMyConfigCommandEdit(syntheticMsg, deps)
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
			ChatID:    chatID,    // Store context
			MessageID: messageID, // Store context
			Action:    newStateAction,
		})
		edit := tgbotapi.NewEditMessageText(chatID, messageID, promptText)
		// Keep the main config keyboard but maybe add a cancel button?
		// For simplicity, just update text and rely on user sending text.
		// Alternatively, remove the keyboard while waiting for text:
		edit.ReplyMarkup = nil
		deps.Bot.Send(edit)
		deps.Bot.Request(answer) // Answer callback
		return                   // Waiting for user text input
	}

	// If an update was attempted directly and resulted in error
	if updateErr != nil {
		deps.Logger.Error("Failed to update user config from callback", zap.Error(updateErr), zap.Int64("user_id", userID), zap.String("data", data))
		if answer.Text == "" {
			answer.Text = "更新配置失败"
		}
		deps.Bot.Request(answer) // Send error feedback
	}

	// If we reached here without returning, it implies a direct callback action
	// (like reset or image size set) completed. State should be cleared.
	// Except for the cases that returned early (like waiting for text input or selection).
	deps.StateManager.ClearState(userID)
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
	settingsText := settingsBuilder.String()

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置图片尺寸", "config_set_imagesize")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置推理步数", "config_set_infsteps")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("设置 Guidance Scale", "config_set_guidscale")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("恢复默认设置", "config_reset_defaults")),
	)

	// Edit the existing message
	edit := tgbotapi.NewEditMessageText(chatID, messageID, settingsText)
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
	// Base LoRAs remain admin-only for selection for now
	visibleBaseLoras := deps.BaseLoRA

	var rows [][]tgbotapi.InlineKeyboardButton
	maxButtonsPerRow := 2

	// --- Standard Visible LoRAs ---
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

	// --- Base LoRAs (Admins only for selection) ---
	if deps.Authorizer.IsAdmin(state.UserID) && len(visibleBaseLoras) > 0 {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("--- Base LoRAs (Admin) ---", "lora_noop")))
		currentRow = []tgbotapi.InlineKeyboardButton{}
		for _, lora := range visibleBaseLoras {
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
			button := tgbotapi.NewInlineKeyboardButtonData(buttonText, "lora_select_"+lora.ID)
			currentRow = append(currentRow, button)
			if len(currentRow) == maxButtonsPerRow {
				rows = append(rows, tgbotapi.NewInlineKeyboardRow(currentRow...))
				currentRow = []tgbotapi.InlineKeyboardButton{}
			}
		}
		if len(currentRow) > 0 {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(currentRow...))
		}
	}

	// --- Confirm/Cancel Buttons ---
	if len(visibleLoras) > 0 || (deps.Authorizer.IsAdmin(state.UserID) && len(visibleBaseLoras) > 0) || len(state.SelectedLoras) > 0 {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🚀 生成图片", "lora_confirm"),
			tgbotapi.NewInlineKeyboardButtonData("❌ 取消", "lora_cancel"),
		))
	} else {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("❌ 取消", "lora_cancel"),
		))
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	// Construct the prompt text using strings.Builder
	var loraPromptBuilder strings.Builder
	loraPromptBuilder.WriteString("请选择您想使用的 LoRA 风格")
	if len(state.SelectedLoras) > 0 {
		loraPromptBuilder.WriteString(fmt.Sprintf(" (已选: %s)", strings.Join(state.SelectedLoras, ", ")))
	}
	loraPromptBuilder.WriteString(":\nPrompt: ```\n")
	loraPromptBuilder.WriteString(state.OriginalCaption)
	loraPromptBuilder.WriteString("\n```")
	loraPrompt := loraPromptBuilder.String()

	// Send or Edit the message
	var msg tgbotapi.Chattable
	if edit && messageID != 0 { // Ensure messageID is valid for editing
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, loraPrompt)
		editMsg.ParseMode = tgbotapi.ModeMarkdown
		editMsg.ReplyMarkup = &keyboard
		msg = editMsg
	} else {
		newMsg := tgbotapi.NewMessage(chatID, loraPrompt)
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

// DEPRECATED or needs update: Use findLoraByID instead if callbacks use ID
func findLoraName(loraName string, loras []LoraConfig) LoraConfig {
	for _, lora := range loras {
		if lora.Name == loraName {
			return lora
		}
	}
	return LoraConfig{} // Return empty struct if not found
}

func GenerateImagesForUser(userState *UserState, deps BotDeps) {
	userID := userState.UserID
	chatID := userState.ChatID
	originalMessageID := userState.MessageID
	deps.StateManager.ClearState(userID)
	if chatID == 0 || originalMessageID == 0 {
		deps.Logger.Error("GenerateImagesForUser called with invalid state", zap.Int64("userID", userID), zap.Int64("chatID", chatID), zap.Int("messageID", originalMessageID))
		deps.Bot.Send(tgbotapi.NewMessage(userID, "❌ 生成失败：内部状态错误，请重试。"))
		return
	}

	// --- Check balance and Deduct --- // Should happen *before* submitting
	if deps.BalanceManager != nil {
		canProceed, deductErr := deps.BalanceManager.CheckAndDeduct(userID)
		if !canProceed {
			errMsg := "❌ 生成失败：余额不足或扣费失败。"
			if deductErr != nil && strings.Contains(deductErr.Error(), "insufficient balance") {
				// Extract needed/current balance if possible from error or re-query
				currentBal := deps.BalanceManager.GetBalance(userID)
				neededBal := deps.BalanceManager.GetCost() // Use the new GetCost() method
				errMsg = fmt.Sprintf(errMsgInsufficientBalance, neededBal, currentBal)
			} else if deductErr != nil {
				errMsg = fmt.Sprintf("❌ 扣费失败: %s", deductErr.Error())
			}
			deps.Logger.Warn("Balance check/deduction failed", zap.Int64("user_id", userID), zap.Error(deductErr))
			edit := tgbotapi.NewEditMessageText(chatID, originalMessageID, errMsg)
			edit.ReplyMarkup = nil
			deps.Bot.Send(edit)
			return
		}
		// Balance deducted successfully if we reach here
		deps.Logger.Info("Balance checked and deducted", zap.Int64("user_id", userID))
	}

	// --- Get User/Default Generation Config --- // (Keep existing logic)
	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		deps.Logger.Error("Failed to get user config before generation", zap.Error(err), zap.Int64("user_id", userID))
	}
	defaultCfg := deps.Config.DefaultGenerationSettings
	// Directly prepare the falapi.GenerateRequest fields
	prompt := userState.OriginalCaption
	imageSize := defaultCfg.ImageSize
	numInferenceSteps := defaultCfg.NumInferenceSteps
	guidanceScale := defaultCfg.GuidanceScale
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
	}

	// --- Prepare LoRA parameters (use falapi.LoraWeight) ---
	selectedLoraDetails := []falapi.LoraWeight{} // Use correct type
	allAvailableLoras := append(deps.LoRA, deps.BaseLoRA...)
	for _, selectedName := range userState.SelectedLoras {
		found := false
		for _, lora := range allAvailableLoras {
			if lora.Name == selectedName {
				selectedLoraDetails = append(selectedLoraDetails, falapi.LoraWeight{
					Path:  lora.URL, // Assuming Bot's LoraConfig URL is the path for Fal
					Scale: lora.Weight,
				})
				found = true
				break
			}
		}
		if !found {
			deps.Logger.Warn("Selected LoRA name not found", zap.String("loraName", selectedName), zap.Int64("user_id", userID))
		}
	}
	if len(selectedLoraDetails) == 0 {
		deps.Logger.Error("No valid LoRAs selected or found", zap.Int64("user_id", userID), zap.Strings("selectedNames", userState.SelectedLoras))
		errMsg := "❌ 生成失败：未找到有效的 LoRA 配置。"
		edit := tgbotapi.NewEditMessageText(chatID, originalMessageID, errMsg)
		deps.Bot.Send(edit)
		return
	}

	// --- Submit generation request (use falapi.GenerateRequest) ---
	submitTime := time.Now()
	request := falapi.GenerateRequest{
		Prompt:            prompt,
		Loras:             selectedLoraDetails, // Use the correct type
		ImageSize:         imageSize,
		NumInferenceSteps: numInferenceSteps,
		GuidanceScale:     guidanceScale,
		// Set other fields like NumImages, EnableSafetyChecker if needed
		EnableSafetyChecker: false, // Example: Explicitly disable safety checker
		NumImages:           1,     // Example: Generate 1 image
	}
	// Call the correct SubmitGenerationRequest signature
	requestID, err := deps.FalClient.SubmitGenerationRequest(request.Prompt, request.Loras, nil, fmt.Sprintf("%v", request.ImageSize), request.NumInferenceSteps, request.GuidanceScale) // Pass nil for loraNames if not used by this specific function signature

	if err != nil {
		// Log details, edit message with generic error
		editWithGenericError(chatID, originalMessageID, userID, "SubmitGenerationRequest", err, deps)
		// Consider attempting refund if balance was deducted
		// if deps.BalanceManager != nil { deps.BalanceManager.AddBalance(userID, deps.BalanceManager.cost) }
		return
	}
	deps.Logger.Info("Submitted generation task", zap.Int64("user_id", userID), zap.String("request_id", requestID))

	// Update status message
	statusUpdate := fmt.Sprintf("⏳ 生成任务已提交 (ID: ...%s)。正在轮询结果...", truncateID(requestID))
	editStatus := tgbotapi.NewEditMessageText(chatID, originalMessageID, statusUpdate)
	deps.Bot.Send(editStatus)

	// --- Poll for result --- // (Keep existing logic, assuming PollForGenerateResult exists)
	pollInterval := 5 * time.Second
	generationTimeout := 5 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), generationTimeout)
	defer cancel()
	// Ensure PollForGenerateResult is the correct function name -> Should be PollForResult
	result, err := deps.FalClient.PollForResult(ctx, requestID, deps.Config.APIEndpoints.FluxLora, pollInterval)
	if err != nil {
		// Log details, provide more specific error if possible
		errText := fmt.Sprintf("❌ 获取生成结果失败: %s", err.Error())
		if errors.Is(err, context.DeadlineExceeded) {
			errText = "❌ 获取生成结果超时，任务可能仍在后台运行，请稍后检查或联系管理员。"
		} else if strings.Contains(err.Error(), "generation failed:") {
			// Try to extract Fal API error message
			errText = fmt.Sprintf("❌ 生成失败: %s", strings.TrimPrefix(err.Error(), "generation failed: "))
		}
		deps.Logger.Error("Polling/generation failed", zap.Error(err), zap.Int64("user_id", userID), zap.String("request_id", requestID))
		editErr := tgbotapi.NewEditMessageText(chatID, originalMessageID, errText)
		editErr.ReplyMarkup = nil
		deps.Bot.Send(editErr)
		return
	}

	// --- Success --- //
	duration := time.Since(submitTime)
	deps.Logger.Info("Generation successful", zap.Int64("user_id", userID), zap.String("request_id", requestID), zap.Duration("duration", duration), zap.Int("image_count", len(result.Images)))

	// Balance already deducted

	// Send results
	if len(result.Images) > 0 {
		var mediaGroup []interface{}
		finalCaption := fmt.Sprintf("🎨 使用 LoRA: %s\n⏱️ 耗时: %.1fs", strings.Join(userState.SelectedLoras, ", "), duration.Seconds())
		if deps.BalanceManager != nil {
			// Show balance AFTER generation/deduction
			finalCaption += fmt.Sprintf("\n💰 余额: %.2f", deps.BalanceManager.GetBalance(userID))
		}
		for i, img := range result.Images {
			photo := tgbotapi.NewInputMediaPhoto(tgbotapi.FileURL(img.URL))
			if i == 0 {
				photo.Caption = finalCaption
				photo.ParseMode = tgbotapi.ModeMarkdown
			}
			mediaGroup = append(mediaGroup, photo)
			if len(mediaGroup) == 10 {
				mediaMessage := tgbotapi.NewMediaGroup(chatID, mediaGroup)
				if _, err := deps.Bot.Send(mediaMessage); err != nil {
					deps.Logger.Error("Failed to send image group", zap.Error(err), zap.Int64("user_id", userID))
				}
				mediaGroup = []interface{}{}
			}
		}
		if len(mediaGroup) > 0 {
			mediaMessage := tgbotapi.NewMediaGroup(chatID, mediaGroup)
			if _, err := deps.Bot.Send(mediaMessage); err != nil {
				deps.Logger.Error("Failed to send final image group", zap.Error(err), zap.Int64("user_id", userID))
				deps.Bot.Send(tgbotapi.NewMessage(chatID, "❌ 发送部分图片时出错。"))
			}
		}
		// Delete the status message *after* sending results
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, originalMessageID)
		if _, errDel := deps.Bot.Request(deleteMsg); errDel != nil {
			deps.Logger.Warn("Failed to delete status message after sending results", zap.Error(errDel), zap.Int64("user_id", userID), zap.Int("message_id", originalMessageID))
		}
	} else {
		deps.Logger.Warn("Generation successful but no images returned", zap.Int64("user_id", userID), zap.String("request_id", requestID))
		errMsg := "✅ 生成完成，但未返回任何图片。"
		edit := tgbotapi.NewEditMessageText(chatID, originalMessageID, errMsg)
		edit.ReplyMarkup = nil
		deps.Bot.Send(edit)
	}
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
	settingsText := settingsBuilder.String()

	// Create inline keyboard for modification
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("设置图片尺寸", "config_set_imagesize"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("设置推理步数", "config_set_infsteps"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("设置 Guidance Scale", "config_set_guidscale"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("恢复默认设置", "config_reset_defaults"),
		),
	)

	reply := tgbotapi.NewMessage(chatID, settingsText)
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

	default:
		deps.Logger.Warn("Received text input in unexpected config state", zap.String("action", action), zap.Int64("user_id", userID))
		deps.Bot.Send(tgbotapi.NewMessage(chatID, "内部错误：未知的配置状态。"))
		deps.StateManager.ClearState(userID)
		return
	}

	if updateErr != nil {
		sendGenericError(chatID, userID, "SetConfigValue", updateErr, deps)
	} else {
		deps.Logger.Info("User config updated successfully", zap.Int64("user_id", userID), zap.String("action", action))
		deps.Bot.Send(tgbotapi.NewMessage(chatID, "✅ 配置已更新！"))
		syntheticMsg := &tgbotapi.Message{From: message.From, Chat: message.Chat}
		HandleMyConfigCommand(syntheticMsg, deps)
	}
	deps.StateManager.ClearState(userID) // Clear state after successful update or unrecoverable error
}

// HandleHelpCommand sends the help message.
func HandleHelpCommand(chatID int64, deps BotDeps) {
	helpText := `
*欢迎使用 Flux LoRA 图片生成 Bot！* 🎨

你可以通过以下方式使用我：

1.  *发送图片*：我会自动描述这张图片，然后你可以确认或修改描述，并选择 LoRA 风格来生成新的图片。
2.  *直接发送文本描述*：我会直接使用你的文本作为提示词 (Prompt)，让你选择 LoRA 风格并生成图片。

*可用命令*:
/start - 显示欢迎信息
/help - 显示此帮助信息
/loras - 查看你当前可用的 LoRA 风格列表
/myconfig - 查看和修改你的个性化图片生成参数（尺寸、步数等）
/balance - 查询你当前的生成点数余额 (如果启用了此功能)
/version - 查看当前 Bot 的版本信息

*生成流程*:
- 发送图片或文本后，我会提示你选择 LoRA 风格。
- 点击 LoRA 名称按钮进行选择/取消选择。
- 选择完毕后，点击"生成图片"按钮。
- 生成过程可能需要一些时间，请耐心等待。

*提示*:
- 高质量、清晰的描述有助于生成更好的图片。
- 尝试不同的 LoRA 风格组合！

祝你使用愉快！✨
`
	reply := tgbotapi.NewMessage(chatID, helpText)
	reply.ParseMode = tgbotapi.ModeMarkdown
	deps.Bot.Send(reply)
}
