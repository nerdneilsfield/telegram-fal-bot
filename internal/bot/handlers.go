package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

func HandleUpdate(update tgbotapi.Update, deps BotDeps) {
	defer func() {
		if r := recover(); r != nil {
			errMsg := fmt.Sprintf("%v", r)
			stackTrace := string(debug.Stack())
			deps.Logger.Error("Panic recovered in HandleUpdate", zap.Any("panic_value", errMsg), zap.String("stack", stackTrace))

			// Try to notify user/admin about the panic
			var chatID int64
			var userID int64
			var userLang *string // Get user language for panic messages
			if update.Message != nil {
				chatID = update.Message.Chat.ID
				userID = update.Message.From.ID
				userLang = getUserLanguagePreference(userID, deps)
			} else if update.CallbackQuery != nil {
				userID = update.CallbackQuery.From.ID
				userLang = getUserLanguagePreference(userID, deps)
				if update.CallbackQuery.Message != nil {
					chatID = update.CallbackQuery.Message.Chat.ID
				}
			}

			if chatID != 0 {
				if deps.Authorizer.IsAdmin(userID) {
					// Send detailed panic to admin - Use I18n
					detailedMsg := deps.I18n.T(userLang, "error_panic_admin",
						"userID", userID,
						"error", errMsg,
						"stack", stackTrace,
					)
					// detailedMsg := fmt.Sprintf("☢️ PANIC RECOVERED ☢️\nUser: %d\nError: %s\n\nTraceback:\n```\n%s\n```", userID, errMsg, stackTrace)
					const maxLen = 4090
					if len(detailedMsg) > maxLen {
						detailedMsg = detailedMsg[:maxLen] + "\n...(truncated)```"
					}
					msg := tgbotapi.NewMessage(chatID, detailedMsg)
					// Use ModeMarkdown for panic message as well, simpler
					msg.ParseMode = tgbotapi.ModeMarkdown
					deps.Bot.Send(msg)
				} else {
					// Send generic error to non-admin - Use I18n
					deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "error_generic")))
					// deps.Bot.Send(tgbotapi.NewMessage(chatID, errMsgGeneric))
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
	userLang := getUserLanguagePreference(userID, deps)

	// DO NOT Clear state at the beginning. Clear it specifically when needed.

	// 命令处理
	if message.IsCommand() {
		switch message.Command() {
		case "start":
			HandleStartCommand(chatID, deps)
		case "help": // Handle /help command
			HandleHelpCommand(chatID, deps) // Help command now handles its own ParseMode
		case "balance":
			HandleBalanceCommand(message, deps)
		case "loras":
			HandleLorasCommand(chatID, userID, deps)
		case "version":
			HandleVersionCommand(chatID, deps)
		case "myconfig":
			HandleMyConfigCommand(message, deps) // Config command handles its own ParseMode
		case "set":
			HandleSetCommand(message, deps)
		case "cancel":
			HandleCancelCommand(message, deps)
		case "log":
			HandleLogCommand(chatID, userID, deps)
		case "shortlog":
			HandleShortLogCommand(chatID, userID, deps)
		default:
			// Use I18n for unknown command message
			reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "unknown_command"))
			deps.Bot.Send(reply)
		}
		return // Return after handling command
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
	userLang := getUserLanguagePreference(userID, deps)

	// 1. Get image URL from Telegram
	if len(message.Photo) == 0 {
		deps.Logger.Warn("Photo message received but no photo data", zap.Int64("user_id", userID))
		deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "photo_process_fail_no_data")))
		return
	}
	photo := message.Photo[len(message.Photo)-1] // Highest resolution
	fileConfig := tgbotapi.FileConfig{FileID: photo.FileID}
	file, err := deps.Bot.GetFile(fileConfig)
	if err != nil {
		deps.Logger.Error("Failed to get file", zap.Error(err), zap.Int64("user_id", userID))
		deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "photo_process_fail_no_data")))
		return
	}
	imageURL := file.Link(deps.Bot.Token)

	// 2. Send initial "Submitting..." message
	var msgIDToEdit int
	waitMsg := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "photo_submit_captioning"))
	sentMsg, err := deps.Bot.Send(waitMsg)
	if err == nil && sentMsg.MessageID != 0 {
		msgIDToEdit = sentMsg.MessageID
	} else if err != nil {
		deps.Logger.Error(deps.I18n.T(userLang, "photo_fail_send_wait_msg"), zap.Error(err), zap.Int64("user_id", userID))
	}

	// 3. Start captioning process in a Goroutine
	go func(imgURL string, originalChatID int64, originalUserID int64, editMsgID int) {
		// Get user lang inside goroutine as well, in case default changed?
		// Or assume the lang preference at the start of the handler is sufficient.
		// Let's use the initial userLang for messages within this goroutine.
		currentUserLang := userLang

		captionEndpoint := deps.Config.APIEndpoints.FlorenceCaption // Get caption endpoint from config
		pollInterval := 5 * time.Second                             // Adjust interval as needed
		captionTimeout := 2 * time.Minute                           // Timeout for captioning

		// 3a. Submit caption request
		requestID, err := deps.FalClient.SubmitCaptionRequest(imgURL)
		if err != nil {
			// Log detailed error, send more specific error to user if possible
			errTextKey := "photo_caption_fail"
			if errors.Is(err, context.DeadlineExceeded) {
				errTextKey = "photo_caption_timeout"
			}
			errText := deps.I18n.T(currentUserLang, errTextKey, "error", err.Error())
			deps.Logger.Error(deps.I18n.T(currentUserLang, "photo_polling_fail"), zap.Error(err), zap.Int64("user_id", originalUserID), zap.String("request_id", requestID))
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
		statusUpdate := deps.I18n.T(currentUserLang, "photo_caption_submitted", "reqID", truncateID(requestID))
		if editMsgID != 0 {
			deps.Bot.Send(tgbotapi.NewEditMessageText(originalChatID, editMsgID, statusUpdate))
		}

		// 3b. Poll for caption result
		ctx, cancel := context.WithTimeout(context.Background(), captionTimeout)
		defer cancel()
		captionText, err := deps.FalClient.PollForCaptionResult(ctx, requestID, captionEndpoint, pollInterval)

		if err != nil {
			// Log detailed error, provide more specific error if possible
			errTextKey := "photo_caption_fail"
			if errors.Is(err, context.DeadlineExceeded) {
				errTextKey = "photo_caption_timeout"
			}
			errText := deps.I18n.T(currentUserLang, errTextKey, "error", err.Error())
			deps.Logger.Error(deps.I18n.T(currentUserLang, "photo_polling_fail"), zap.Error(err), zap.Int64("user_id", originalUserID), zap.String("request_id", requestID))
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
		// Use I18n for text and buttons
		msgText := deps.I18n.T(currentUserLang, "photo_caption_received_prompt", "caption", captionText)
		confirmationKeyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(currentUserLang, "photo_caption_confirm_button"), "caption_confirm"),
				tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(currentUserLang, "photo_caption_cancel_button"), "caption_cancel"),
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
	userLang := getUserLanguagePreference(userID, deps)

	// Send message indicating LoRA selection will start
	waitMsg := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "text_prompt_received"))
	// waitMsg := tgbotapi.NewMessage(chatID, "⏳ Got it! Please select LoRA styles for your prompt...")
	sentMsg, err := deps.Bot.Send(waitMsg)
	if err != nil {
		deps.Logger.Error(deps.I18n.T(userLang, "text_fail_send_wait_msg"), zap.Error(err), zap.Int64("user_id", userID))
		// deps.Logger.Error("Failed to send initial wait message for text prompt", zap.Error(err), zap.Int64("user_id", userID))
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
		deps.Logger.Warn(deps.I18n.T(userLang, "text_warn_keyboard_new_msg"), zap.Int64("user_id", userID))
		// deps.Logger.Warn("Could not send wait message, sending keyboard as new message", zap.Int64("user_id", userID))
		SendLoraSelectionKeyboard(chatID, 0, newState, deps, false) // Send as new message
	}
}

// HandleStartCommand handles the /start command.
func HandleStartCommand(chatID int64, deps BotDeps) {
	userLang := getUserLanguagePreference(chatID, deps) // Get user lang
	reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "welcome"))
	reply.ParseMode = tgbotapi.ModeMarkdown
	deps.Bot.Send(reply)
}

// HandleBalanceCommand handles the /balance command.
func HandleBalanceCommand(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID
	userLang := getUserLanguagePreference(userID, deps) // Get user lang

	if deps.BalanceManager != nil {
		balance := deps.BalanceManager.GetBalance(userID)
		if balance == 0 {
			deps.Logger.Error("Failed to get user balance", zap.Int64("user_id", userID))
			reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "error_generic"))
			deps.Bot.Send(reply)
		} else {
			formattedBalance := fmt.Sprintf("%.2f", balance)
			reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "balance_current", "balance", formattedBalance))
			deps.Bot.Send(reply)
		}
	} else {
		reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "balance_not_enabled"))
		deps.Bot.Send(reply)
	}

	if deps.Authorizer.IsAdmin(userID) {
		go func() {
			reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "balance_admin_checking"))
			msg, err := deps.Bot.Send(reply)
			if err != nil {
				deps.Logger.Error("Failed to send admin balance message", zap.Error(err), zap.Int64("user_id", userID))
				return
			}
			balance, err := deps.FalClient.GetAccountBalance()
			if err != nil {
				deps.Logger.Error("Failed to get account balance", zap.Error(err), zap.Int64("user_id", userID))
				edit := tgbotapi.NewEditMessageText(chatID, msg.MessageID, deps.I18n.T(userLang, "balance_admin_fetch_failed", "error", err.Error()))
				deps.Bot.Send(edit)
			} else {
				formattedAdminBalance := fmt.Sprintf("%.2f", balance)
				edit := tgbotapi.NewEditMessageText(chatID, msg.MessageID, deps.I18n.T(userLang, "balance_admin_actual", "balance", formattedAdminBalance))
				deps.Bot.Send(edit)
			}
		}()
	}
}

// HandleLorasCommand handles the /loras command.
func HandleLorasCommand(chatID int64, userID int64, deps BotDeps) {
	userLang := getUserLanguagePreference(userID, deps) // Get user lang
	visibleLoras := GetUserVisibleLoras(userID, deps)

	var loraList strings.Builder
	if len(visibleLoras) > 0 {
		loraList.WriteString(deps.I18n.T(userLang, "loras_available_title") + "\n")
		for _, lora := range visibleLoras {
			loraList.WriteString(deps.I18n.T(userLang, "loras_item", "name", lora.Name) + "\n")
		}
	} else {
		loraList.WriteString(deps.I18n.T(userLang, "loras_none_available"))
	}

	if deps.Authorizer.IsAdmin(userID) && len(deps.BaseLoRA) > 0 {
		loraList.WriteString(deps.I18n.T(userLang, "loras_base_title_admin") + "\n")
		for _, lora := range deps.BaseLoRA {
			loraList.WriteString(deps.I18n.T(userLang, "loras_item", "name", lora.Name) + "\n")
		}
	}

	reply := tgbotapi.NewMessage(chatID, loraList.String())
	reply.ParseMode = tgbotapi.ModeMarkdown
	deps.Bot.Send(reply)
}

// HandleVersionCommand handles the /version command.
func HandleVersionCommand(chatID int64, deps BotDeps) {
	userLang := getUserLanguagePreference(chatID, deps) // Get user lang
	goVersion := runtime.Version()
	reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "version_info",
		"version", deps.Version,
		"buildDate", deps.BuildDate,
		"goVersion", goVersion))
	reply.ParseMode = tgbotapi.ModeMarkdown
	deps.Bot.Send(reply)
}

// HandleSetCommand handles the /set command (currently placeholder).
func HandleSetCommand(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID
	userLang := getUserLanguagePreference(userID, deps) // Get user lang

	if !deps.Authorizer.IsAdmin(userID) {
		reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "myconfig_command_admin_only"))
		deps.Bot.Send(reply)
		return
	}
	reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "myconfig_command_dev"))
	deps.Bot.Send(reply)
}

// HandleCancelCommand handles the /cancel command.
func HandleCancelCommand(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID
	userLang := getUserLanguagePreference(userID, deps) // Get user lang

	state, exists := deps.StateManager.GetState(userID)
	if exists {
		deps.StateManager.ClearState(userID)
		deps.Logger.Info("User cancelled operation via /cancel", zap.Int64("user_id", userID), zap.String("state", state.Action))
		if state.ChatID != 0 && state.MessageID != 0 {
			edit := tgbotapi.NewEditMessageText(state.ChatID, state.MessageID, deps.I18n.T(userLang, "cancel_state_success"))
			edit.ReplyMarkup = nil // Remove keyboard on cancel
			deps.Bot.Send(edit)
		} else {
			reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "cancel_success"))
			deps.Bot.Send(reply)
		}
	} else {
		reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "cancel_failed"))
		deps.Bot.Send(reply)
	}
}

// HandleHelpCommand sends the help message.
func HandleHelpCommand(chatID int64, deps BotDeps) {
	// Adjusted help text for ModeMarkdown (escape * and `)
	// Use I18n keys for the entire help message
	userLang := getUserLanguagePreference(chatID, deps) // Get user lang

	helpText := strings.Join([]string{
		deps.I18n.T(userLang, "help_title"),
		"", // Empty line for spacing
		deps.I18n.T(userLang, "help_usage"),
		"", // Empty line
		deps.I18n.T(userLang, "help_usage_image"),
		deps.I18n.T(userLang, "help_usage_text"),
		"", // Empty line
		deps.I18n.T(userLang, "help_commands_title"),
		deps.I18n.T(userLang, "help_command_start"),
		deps.I18n.T(userLang, "help_command_help"),
		deps.I18n.T(userLang, "help_command_loras"),
		deps.I18n.T(userLang, "help_command_myconfig"),
		deps.I18n.T(userLang, "help_command_balance"),
		deps.I18n.T(userLang, "help_command_version"),
		deps.I18n.T(userLang, "help_command_cancel"),
		deps.I18n.T(userLang, "help_command_set"),
		"", // Empty line
		deps.I18n.T(userLang, "help_flow_title"),
		deps.I18n.T(userLang, "help_flow_step1"),
		deps.I18n.T(userLang, "help_flow_step2"),
		deps.I18n.T(userLang, "help_flow_step3"),
		deps.I18n.T(userLang, "help_flow_step4"),
		"", // Empty line
		deps.I18n.T(userLang, "help_tips_title"),
		deps.I18n.T(userLang, "help_tip1"),
		deps.I18n.T(userLang, "help_tip2"),
		"", // Empty line
		deps.I18n.T(userLang, "help_enjoy"),
	}, "\n")

	reply := tgbotapi.NewMessage(chatID, helpText)
	// Switch back to ModeMarkdown
	reply.ParseMode = tgbotapi.ModeMarkdown
	deps.Bot.Send(reply)
}

func HandleLogCommand(chatID int64, userID int64, deps BotDeps) {
	userLang := getUserLanguagePreference(userID, deps) // Get user lang

	// 1. Check if user is admin
	if !deps.Authorizer.IsAdmin(userID) {
		reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "log_admin_only"))
		deps.Bot.Send(reply)
		return
	}

	// 2. Check if file logging is enabled (by checking if the path is set)
	logFilePath := deps.Config.LogConfig.File
	if logFilePath == "" {
		reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "log_file_disabled"))
		deps.Bot.Send(reply)
		return
	}

	// 3. Send status message
	waitMsg := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "log_sending"))
	deps.Bot.Send(waitMsg)

	// 4. Prepare and send the document
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(logFilePath))
	_, err := deps.Bot.Send(doc)
	if err != nil {
		deps.Logger.Error("Failed to send log file", zap.Error(err), zap.String("path", logFilePath), zap.Int64("user_id", userID))
		// Optionally send an error message back to the user
		errorMsg := deps.I18n.T(userLang, "log_send_error", "error", err.Error())
		deps.Bot.Send(tgbotapi.NewMessage(chatID, errorMsg))
	}
}

func HandleShortLogCommand(chatID int64, userID int64, deps BotDeps) {
	userLang := getUserLanguagePreference(userID, deps) // Get user lang

	// 1. Check if user is admin
	if !deps.Authorizer.IsAdmin(userID) {
		reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "log_admin_only"))
		deps.Bot.Send(reply)
		return
	}

	// 2. Check if file logging is enabled (by checking if the path is set)
	logFilePath := deps.Config.LogConfig.File
	if logFilePath == "" {
		reply := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "log_file_disabled"))
		deps.Bot.Send(reply)
		return
	}

	// 3. Send status message
	waitMsg := tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "log_sending_short"))
	deps.Bot.Send(waitMsg)

	// 4. Read the log file content
	// TODO: This reads the entire file into memory, which might be inefficient for very large log files.
	// A more robust solution would read chunks from the end of the file.
	content, err := os.ReadFile(logFilePath)
	if err != nil {
		deps.Logger.Error("Failed to read log file for shortlog", zap.Error(err), zap.String("path", logFilePath), zap.Int64("user_id", userID))
		errorMsg := deps.I18n.T(userLang, "log_read_error", "error", err.Error())
		deps.Bot.Send(tgbotapi.NewMessage(chatID, errorMsg))
		return
	}

	// 5. Get the last 100 lines
	lines := strings.Split(string(content), "\n")
	numLines := len(lines)
	startLine := 0
	if numLines > 100 {
		startLine = numLines - 100
	}
	// Handle potential trailing newline causing an empty last element if needed
	if lines[numLines-1] == "" {
		lines = lines[:numLines-1] // Exclude empty last line from count
		numLines--
		if numLines > 100 {
			startLine = numLines - 100
		} else {
			startLine = 0
		}
	}
	shortLogContent := strings.Join(lines[startLine:], "\n")
	actualLinesSent := numLines - startLine

	// 6. Create a temporary file
	tempFile, err := os.CreateTemp("", "shortlog-*.log")
	if err != nil {
		deps.Logger.Error("Failed to create temp file for short log", zap.Error(err), zap.Int64("user_id", userID))
		errorMsg := deps.I18n.T(userLang, "log_temp_file_error", "error", err.Error())
		deps.Bot.Send(tgbotapi.NewMessage(chatID, errorMsg))
		return
	}
	// Ensure cleanup happens even if subsequent steps fail
	defer os.Remove(tempFile.Name())

	// 7. Write the short log content to the temporary file
	_, err = tempFile.WriteString(shortLogContent)
	if err != nil {
		deps.Logger.Error("Failed to write to temp file for short log", zap.Error(err), zap.String("tempfile", tempFile.Name()), zap.Int64("user_id", userID))
		tempFile.Close() // Close before attempting remove
		errorMsg := deps.I18n.T(userLang, "log_write_error", "error", err.Error())
		deps.Bot.Send(tgbotapi.NewMessage(chatID, errorMsg))
		return
	}
	err = tempFile.Close() // Close the file to ensure data is flushed
	if err != nil {
		deps.Logger.Error("Failed to close temp file for short log", zap.Error(err), zap.String("tempfile", tempFile.Name()), zap.Int64("user_id", userID))
		// Log only, maybe not critical to inform user
	}

	// 8. Send the temporary file as a document
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(tempFile.Name()))
	doc.Caption = deps.I18n.T(userLang, "shortlog_caption", "lines", actualLinesSent)
	_, err = deps.Bot.Send(doc)
	if err != nil {
		deps.Logger.Error("Failed to send short log document", zap.Error(err), zap.String("tempfile", tempFile.Name()), zap.Int64("user_id", userID))
		errorMsg := deps.I18n.T(userLang, "log_send_error", "error", err.Error())
		deps.Bot.Send(tgbotapi.NewMessage(chatID, errorMsg))
	}
}
