package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"

	st "github.com/nerdneilsfield/telegram-fal-bot/internal/storage"
)

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
		// Get default lang for this internal error message
		answer := tgbotapi.NewCallback(callbackQuery.ID, deps.I18n.T(nil, "callback_error_nil_message"))
		deps.Bot.Request(answer)
		return
	}
	data := callbackQuery.Data

	// Get user language preference early
	userLang := getUserLanguagePreference(userID, deps)

	deps.Logger.Info("Callback received", zap.Int64("user_id", userID), zap.String("data", data), zap.Int64("chat_id", chatID), zap.Int("message_id", messageID))

	answer := tgbotapi.NewCallback(callbackQuery.ID, "") // Prepare default answer

	// --- Admin User Management Callbacks ---
	if strings.HasPrefix(data, "admin_") {
		HandleAdminCallback(callbackQuery, deps)
		return
	}

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
		answer.Text = deps.I18n.T(userLang, "callback_error_state_expired")
		// answer.Text = errMsgStateExpired
		deps.Bot.Request(answer)
		edit := tgbotapi.NewEditMessageText(chatID, messageID, deps.I18n.T(userLang, "callback_error_state_expired"))
		// edit := tgbotapi.NewEditMessageText(chatID, messageID, errMsgStateExpired)
		edit.ReplyMarkup = nil
		deps.Bot.Send(edit)
		return
	}

	// Ensure state has chat/message ID
	if state.ChatID == 0 || state.MessageID == 0 {
		deps.Logger.Error("State is missing ChatID or MessageID during callback", zap.Int64("userID", userID), zap.Int64("stateChatID", state.ChatID), zap.Int("stateMessageID", state.MessageID))
		// Attempt to use current callback message info as fallback? Risky.
		// For now, treat as error.
		answer.Text = deps.I18n.T(userLang, "callback_error_state_missing_context")
		deps.Bot.Request(answer)
		edit := tgbotapi.NewEditMessageText(chatID, messageID, deps.I18n.T(userLang, "callback_error_state_missing_context")) // Edit the current message
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
				answer.Text = deps.I18n.T(userLang, "lora_select_invalid_id")
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
				maxLoras := deps.Config.APIEndpoints.MaxLoras
				if maxLoras <= 0 {
					maxLoras = 2
				}
				if len(state.SelectedBaseLoras)+len(state.SelectedLoras)+1 > maxLoras {
					answer.Text = deps.I18n.T(userLang, "lora_select_limit_reached", "max", maxLoras)
					deps.Bot.Request(answer)
					return
				}
				newSelection = append(newSelection, selectedLora.Name)
			}
			state.SelectedLoras = newSelection
			deps.StateManager.SetState(userID, state) // Save updated selection

			// Update keyboard
			ansText := deps.I18n.T(userLang, "lora_select_standard_selected", "selection", strings.Join(state.SelectedLoras, ", "))
			if len(state.SelectedLoras) == 0 {
				ansText = deps.I18n.T(userLang, "lora_select_standard_none_selected")
			}
			answer.Text = ansText
			deps.Bot.Request(answer)
			// Re-send the standard LoRA keyboard with updated selections
			// SendLoraSelectionKeyboard handles ParseMode internally now
			SendLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true)

		} else if data == "lora_standard_done" { // Finished selecting standard LoRAs
			if len(state.SelectedLoras) == 0 {
				answer.Text = deps.I18n.T(userLang, "lora_select_standard_error_none_selected")
				deps.Bot.Request(answer)
				return
			}
			answer.Text = deps.I18n.T(userLang, "lora_select_standard_done_prompt")
			deps.Bot.Request(answer)

			// Update state and show Base LoRA keyboard
			state.Action = "awaiting_base_lora_selection"
			deps.StateManager.SetState(userID, state)
			// SendBaseLoraSelectionKeyboard handles ParseMode internally now
			SendBaseLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true) // New function needed

		} else if data == "lora_cancel" {
			// ... (cancel handling) ...
			answer.Text = deps.I18n.T(userLang, "lora_select_cancel_success")
			deps.Bot.Request(answer)
			deps.StateManager.ClearState(userID)
			edit := tgbotapi.NewEditMessageText(state.ChatID, state.MessageID, deps.I18n.T(userLang, "lora_select_cancel_success"))
			edit.ReplyMarkup = nil // Clear keyboard
			deps.Bot.Send(edit)
		} else if data == "lora_noop" {
			// Do nothing, just answer the callback
			deps.Bot.Request(answer)
		} else {
			answer.Text = deps.I18n.T(userLang, "lora_select_unknown_action")
			deps.Bot.Request(answer)
		}

	case "awaiting_base_lora_selection": // Step 2: Selecting (optional) Base LoRA
		if strings.HasPrefix(data, "base_lora_select_") {
			loraID := strings.TrimPrefix(data, "base_lora_select_")
			// Find the selected Base LoRA by ID
			selectedBaseLora := findLoraByID(loraID, deps.BaseLoRA)

			if selectedBaseLora.ID == "" { // Not found
				answer.Text = deps.I18n.T(userLang, "base_lora_select_invalid_id")
				deps.Bot.Request(answer)
				deps.Logger.Warn("Invalid base lora ID selected", zap.String("loraID", loraID), zap.Int64("user_id", userID))
				return
			}

			found := false
			newSelection := []string{}
			for _, name := range state.SelectedBaseLoras {
				if name == selectedBaseLora.Name {
					found = true
				} else {
					newSelection = append(newSelection, name)
				}
			}
			if !found {
				maxLoras := deps.Config.APIEndpoints.MaxLoras
				if maxLoras <= 0 {
					maxLoras = 2
				}
				if len(state.SelectedBaseLoras)+len(state.SelectedLoras)+1 > maxLoras {
					answer.Text = deps.I18n.T(userLang, "lora_select_limit_reached", "max", maxLoras)
					deps.Bot.Request(answer)
					return
				}
				newSelection = append(newSelection, selectedBaseLora.Name)
				answer.Text = deps.I18n.T(userLang, "base_lora_select_selected", "name", selectedBaseLora.Name)
			} else {
				answer.Text = deps.I18n.T(userLang, "base_lora_select_deselected")
			}
			state.SelectedBaseLoras = newSelection
			deps.StateManager.SetState(userID, state)
			deps.Bot.Request(answer)
			// Update keyboard to show selection
			// SendBaseLoraSelectionKeyboard handles ParseMode internally now
			SendBaseLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true)

		} else if data == "base_lora_skip" {
			state.SelectedBaseLoras = []string{}
			deps.StateManager.SetState(userID, state)
			answer.Text = deps.I18n.T(userLang, "base_lora_skip_success")
			deps.Bot.Request(answer)
			// Update keyboard
			// SendBaseLoraSelectionKeyboard handles ParseMode internally now
			SendBaseLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true)

		} else if data == "lora_confirm_generate" {
			// Final confirmation step
			if len(state.SelectedLoras) == 0 {
				// Should not happen if previous step enforced selection, but check again
				answer.Text = deps.I18n.T(userLang, "base_lora_confirm_error_no_standard")
				deps.Bot.Request(answer)
				return
			}

			answer.Text = deps.I18n.T(userLang, "base_lora_confirm_submitting")
			deps.Bot.Request(answer)

			// Build confirmation message using i18n keys
			var confirmBuilder strings.Builder
			standardLorasStr := fmt.Sprintf("`%s`", strings.Join(state.SelectedLoras, "`, `"))
			if len(state.SelectedBaseLoras) > 0 {
				baseLoraStr := strings.Join(state.SelectedBaseLoras, ", ")
				confirmBuilder.WriteString(deps.I18n.T(userLang, "base_lora_confirm_prep_text_with_base",
					"count", len(state.SelectedLoras),
					"standardLoras", standardLorasStr,
					"baseLora", baseLoraStr))
			} else {
				confirmBuilder.WriteString(deps.I18n.T(userLang, "base_lora_confirm_prep_text",
					"count", len(state.SelectedLoras),
					"standardLoras", standardLorasStr))
			}
			confirmBuilder.WriteString("\n")
			confirmBuilder.WriteString(deps.I18n.T(userLang, "base_lora_confirm_prompt", "prompt", state.OriginalCaption))
			confirmText := confirmBuilder.String()

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

	case "awaiting_caption_confirmation": // Handle callbacks after caption is received
		if data == "caption_confirm" {
			// User confirmed the caption, move to LoRA selection
			answer.Text = deps.I18n.T(userLang, "text_prompt_received") // Reuse "Select LoRA" message
			deps.Bot.Request(answer)

			// Update state for LoRA selection
			state.Action = "awaiting_lora_selection"
			// Keep OriginalCaption, reset SelectedLoras
			state.SelectedLoras = []string{}
			state.SelectedBaseLoras = []string{} // Clear base lora selection too
			deps.StateManager.SetState(userID, state)

			// Send the standard LoRA selection keyboard, editing the confirmation message
			SendLoraSelectionKeyboard(state.ChatID, state.MessageID, state, deps, true)

		} else if data == "caption_cancel" {
			// User cancelled after caption
			answer.Text = deps.I18n.T(userLang, "lora_select_cancel_success") // Reuse cancel message
			deps.Bot.Request(answer)
			deps.StateManager.ClearState(userID)
			// Edit the original message to show cancellation
			edit := tgbotapi.NewEditMessageText(state.ChatID, state.MessageID, deps.I18n.T(userLang, "lora_select_cancel_success"))
			edit.ReplyMarkup = nil // Clear keyboard
			deps.Bot.Send(edit)
		} else {
			// Unknown action in this state
			answer.Text = deps.I18n.T(userLang, "lora_select_unknown_action")
			deps.Bot.Request(answer)
		}

	default:
		deps.Logger.Warn("Callback received for unhandled action", zap.String("action", state.Action), zap.Int64("user_id", userID), zap.String("data", data))
		// Use I18n
		answer.Text = deps.I18n.T(userLang, "unhandled_state_error")
		// answer.Text = "未知状态或操作"
		deps.Bot.Request(answer)
	}
}

// Handles callbacks starting with "config_"
func HandleConfigCallback(callbackQuery *tgbotapi.CallbackQuery, deps BotDeps) {
	userID := callbackQuery.From.ID
	// Ensure message context exists
	if callbackQuery.Message == nil {
		deps.Logger.Error("Config callback query message is nil", zap.Int64("user_id", userID), zap.String("data", callbackQuery.Data))
		// Get default language for this internal error message
		answer := tgbotapi.NewCallback(callbackQuery.ID, deps.I18n.T(nil, "callback_error_nil_message"))
		deps.Bot.Request(answer)
		return
	}
	chatID := callbackQuery.Message.Chat.ID
	messageID := callbackQuery.Message.MessageID
	data := callbackQuery.Data

	// Get user language preference at the beginning
	userLang := getUserLanguagePreference(userID, deps)

	answer := tgbotapi.NewCallback(callbackQuery.ID, "") // Prepare answer

	// Get current config or initialize a new one
	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID)
	// Check specifically for ErrNoRows, otherwise treat as a real error
	if err != nil && !errors.Is(err, sql.ErrNoRows) { // Use sql.ErrNoRows
		deps.Logger.Error("Failed to get user config during callback", zap.Error(err), zap.Int64("user_id", userID))
		answer.Text = deps.I18n.T(userLang, "config_callback_error_get_config")
		deps.Bot.Request(answer)
		return
	}
	// If err is sql.ErrNoRows, userCfg will be nil. Initialize a new one.
	if userCfg == nil {
		// Initialize with defaults from the main config, as GetUserGenerationConfig now only returns DB values or nil
		defaultCfg := deps.Config.DefaultGenerationSettings
		userCfg = &st.UserGenerationConfig{
			UserID:            userID,
			ImageSize:         defaultCfg.ImageSize,
			NumInferenceSteps: defaultCfg.NumInferenceSteps,
			GuidanceScale:     defaultCfg.GuidanceScale,
			NumImages:         defaultCfg.NumImages,
			Language:          deps.Config.DefaultLanguage, // Use default language from config
		}
		deps.Logger.Debug("Initialized new config for user during callback", zap.Int64("user_id", userID))
	}

	var updateErr error
	var newStateAction string
	var promptText string
	var keyboard *tgbotapi.InlineKeyboardMarkup // Keyboard for text input prompt

	switch data {
	case "config_set_imagesize":
		answer.Text = deps.I18n.T(userLang, "config_callback_select_image_size")
		deps.Bot.Request(answer) // Answer first
		sizes := []string{"square", "portrait_16_9", "landscape_16_9", "portrait_4_3", "landscape_4_3"}
		var rows [][]tgbotapi.InlineKeyboardButton
		// Use the ImageSize directly from userCfg (which has defaults if needed)
		currentSize := userCfg.ImageSize
		for _, size := range sizes {
			buttonText := size
			if size == currentSize {
				// Use I18n for arrow marker
				buttonText = deps.I18n.T(userLang, "button_arrow_right") + " " + size // Indicate current selection
			}
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(buttonText, "config_imagesize_"+size),
			))
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "config_callback_button_back_main"), "config_back_main"),
		))
		kbd := tgbotapi.NewInlineKeyboardMarkup(rows...)
		keyboard = &kbd
		edit := tgbotapi.NewEditMessageText(chatID, messageID, deps.I18n.T(userLang, "config_callback_prompt_image_size"))
		edit.ReplyMarkup = keyboard
		deps.Bot.Send(edit)
		return // Waiting for selection

	case "config_set_infsteps":
		answer.Text = deps.I18n.T(userLang, "config_callback_label_inf_steps")
		newStateAction = "awaiting_config_infsteps"
		promptText = deps.I18n.T(userLang, "config_callback_prompt_inf_steps")
		cancelButtonRow := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "config_callback_button_cancel_input"), "config_cancel_input"))
		kbd := tgbotapi.NewInlineKeyboardMarkup(cancelButtonRow)
		keyboard = &kbd

	case "config_set_guidscale":
		answer.Text = deps.I18n.T(userLang, "config_callback_label_guid_scale")
		newStateAction = "awaiting_config_guidscale"
		promptText = deps.I18n.T(userLang, "config_callback_prompt_guid_scale")
		cancelButtonRow := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "config_callback_button_cancel_input"), "config_cancel_input"))
		kbd := tgbotapi.NewInlineKeyboardMarkup(cancelButtonRow)
		keyboard = &kbd

	case "config_set_numimages":
		answer.Text = deps.I18n.T(userLang, "config_callback_label_num_images")
		newStateAction = "awaiting_config_numimages"
		promptText = deps.I18n.T(userLang, "config_callback_prompt_num_images")
		cancelButtonRow := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "config_callback_button_cancel_input"), "config_cancel_input"))
		kbd := tgbotapi.NewInlineKeyboardMarkup(cancelButtonRow)
		keyboard = &kbd

	case "config_set_language":
		answer.Text = deps.I18n.T(userLang, "config_callback_label_language")
		// answer.Text = "选择语言"
		deps.Bot.Request(answer) // Answer first
		availableLangs := deps.I18n.GetAvailableLanguages()
		var langRows [][]tgbotapi.InlineKeyboardButton
		// Use the Language directly from userCfg
		currentLangCode := userCfg.Language
		for _, langCode := range availableLangs {
			langName, _ := deps.I18n.GetLanguageName(langCode)
			buttonText := fmt.Sprintf("%s (%s)", langName, langCode)
			if langCode == currentLangCode {
				// Use I18n for checkmark
				buttonText = deps.I18n.T(userLang, "button_checkmark") + " " + buttonText // Add checkmark
			}
			langRows = append(langRows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(buttonText, "config_language_"+langCode),
			))
		}
		langRows = append(langRows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "config_callback_button_back_main"), "config_back_main"),
		))
		langKbd := tgbotapi.NewInlineKeyboardMarkup(langRows...)
		edit := tgbotapi.NewEditMessageText(chatID, messageID, deps.I18n.T(userLang, "config_callback_prompt_language")) // "Please select your preferred language:"
		edit.ReplyMarkup = &langKbd
		deps.Bot.Send(edit)
		return // Waiting for language selection

	case "config_reset_defaults":
		// Revert back to using ExecContext for DELETE operation directly
		deleteSQL := "DELETE FROM user_generation_configs WHERE user_id = ?"
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := deps.DB.ExecContext(ctx, deleteSQL, userID)
		cancel() // Release context

		if err != nil {
			// Log and send generic error
			deps.Logger.Error("Failed to delete user config", zap.Error(err), zap.Int64("user_id", userID))
			answer.Text = deps.I18n.T(userLang, "config_callback_reset_fail")
		} else {
			deps.Logger.Info("User config reset to defaults", zap.Int64("user_id", userID))
			answer.Text = deps.I18n.T(userLang, "config_callback_reset_success")

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

	case "config_language_":
		selectedLangCode := strings.TrimPrefix(data, "config_language_")
		// Validate if the selected code is actually available
		availableLangs := deps.I18n.GetAvailableLanguages()
		isValidLang := false
		for _, code := range availableLangs {
			if code == selectedLangCode {
				isValidLang = true
				break
			}
		}

		if !isValidLang {
			deps.Logger.Warn("Invalid language code received in callback", zap.String("code", selectedLangCode), zap.Int64("user_id", userID))
			// Use I18n for the error answer
			answer.Text = deps.I18n.T(userLang, "config_callback_lang_invalid") // Use the new key
			deps.Bot.Request(answer)
			return
		}

		// Assign value directly, not pointer
		userCfg.Language = selectedLangCode
		// Call SetUserGenerationConfig with the struct value
		updateErr = st.SetUserGenerationConfig(deps.DB, *userCfg)
		if updateErr == nil {
			langName, _ := deps.I18n.GetLanguageName(selectedLangCode)
			// Use the *newly selected language* for the confirmation message
			answer.Text = deps.I18n.T(&selectedLangCode, "config_callback_lang_updated", "langName", langName, "langCode", selectedLangCode)
			// Show the updated config menu
			syntheticMsg := &tgbotapi.Message{
				MessageID: messageID,
				From:      callbackQuery.From,
				Chat:      callbackQuery.Message.Chat,
			}
			HandleMyConfigCommand(syntheticMsg, deps)
		} else {
			deps.Logger.Error("Failed to update language preference", zap.Error(updateErr), zap.Int64("user_id", userID), zap.String("language", selectedLangCode))
			// Use the *previous* language for the error message
			userLang := getUserLanguagePreference(userID, deps) // Get potentially old lang for error
			answer.Text = deps.I18n.T(userLang, "config_callback_lang_update_fail")
		}
		deps.Bot.Request(answer)
		deps.StateManager.ClearState(userID)
		return

	case "config_back_main":
		answer.Text = deps.I18n.T(userLang, "config_callback_back_main_label")
		// answer.Text = "返回主菜单"
		deps.Bot.Request(answer)
		syntheticMsg := &tgbotapi.Message{
			MessageID: messageID,
			From:      callbackQuery.From,
			Chat:      callbackQuery.Message.Chat,
		}
		HandleMyConfigCommand(syntheticMsg, deps)
		deps.StateManager.ClearState(userID)
		return

	case "config_cancel_input": // User clicked cancel button while asked for text input
		answer.Text = deps.I18n.T(userLang, "config_callback_cancel_input_label")
		// answer.Text = "取消输入"
		deps.Bot.Request(answer)
		deps.StateManager.ClearState(userID)
		// Show the main config menu again
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
				answer.Text = deps.I18n.T(userLang, "config_callback_image_size_invalid")
				// answer.Text = "无效的尺寸"
				deps.Bot.Request(answer)
				return
			}
			// Assign value directly, not pointer
			userCfg.ImageSize = size
			// Call SetUserGenerationConfig with the struct value
			updateErr = st.SetUserGenerationConfig(deps.DB, *userCfg)
			if updateErr == nil {
				answer.Text = deps.I18n.T(userLang, "config_callback_image_size_success", "size", size)
				syntheticMsg := &tgbotapi.Message{
					MessageID: messageID,
					From:      callbackQuery.From,
					Chat:      callbackQuery.Message.Chat,
				}
				HandleMyConfigCommand(syntheticMsg, deps)
			} else {
				// Log detail, give generic feedback
				deps.Logger.Error("Failed to update image size", zap.Error(updateErr), zap.Int64("user_id", userID), zap.String("size", size))
				answer.Text = deps.I18n.T(userLang, "config_callback_image_size_fail")
			}
			deps.Bot.Request(answer)
			deps.StateManager.ClearState(userID)
			return
		} else if strings.HasPrefix(data, "config_language_") { // Handle language selection
			selectedLangCode := strings.TrimPrefix(data, "config_language_")
			// Validate if the selected code is actually available
			availableLangs := deps.I18n.GetAvailableLanguages()
			isValidLang := false
			for _, code := range availableLangs {
				if code == selectedLangCode {
					isValidLang = true
					break
				}
			}

			if !isValidLang {
				deps.Logger.Warn("Invalid language code received in callback", zap.String("code", selectedLangCode), zap.Int64("user_id", userID))
				// Use I18n for the error answer
				answer.Text = deps.I18n.T(userLang, "config_callback_lang_invalid") // Use the new key
				deps.Bot.Request(answer)
				return
			}

			// Assign value directly, not pointer
			userCfg.Language = selectedLangCode
			// Call SetUserGenerationConfig with the struct value
			updateErr = st.SetUserGenerationConfig(deps.DB, *userCfg)
			if updateErr == nil {
				langName, _ := deps.I18n.GetLanguageName(selectedLangCode)
				// Use the *newly selected language* for the confirmation message
				answer.Text = deps.I18n.T(&selectedLangCode, "config_callback_lang_updated", "langName", langName, "langCode", selectedLangCode)
				// answer.Text = fmt.Sprintf("✅ Language set to %s (%s)", langName, selectedLangCode)
				// Show the updated config menu
				syntheticMsg := &tgbotapi.Message{
					MessageID: messageID,
					From:      callbackQuery.From,
					Chat:      callbackQuery.Message.Chat,
				}
				HandleMyConfigCommand(syntheticMsg, deps)
			} else {
				deps.Logger.Error("Failed to update language preference", zap.Error(updateErr), zap.Int64("user_id", userID), zap.String("language", selectedLangCode))
				// Use the *previous* language for the error message
				// userLang := getUserLanguagePreference(userID, deps) // Get potentially old lang for error
				answer.Text = deps.I18n.T(userLang, "config_callback_lang_update_fail")
				// answer.Text = "❌ Failed to update language preference"
			}
			deps.Bot.Request(answer)
			deps.StateManager.ClearState(userID)
			return
		} else {
			deps.Logger.Warn("Unhandled config callback data", zap.String("data", data), zap.Int64("user_id", userID))
			// Use I18n
			// userLang := getUserLanguagePreference(userID, deps) // Already got userLang at start
			answer.Text = deps.I18n.T(userLang, "config_callback_unhandled")
			// answer.Text = "未知配置操作"
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

// Handles the /myconfig command
func HandleMyConfigCommand(message *tgbotapi.Message, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID

	// Get user language preference first
	userLang := getUserLanguagePreference(userID, deps)

	// Fetch user's config from DB
	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID) // Use aliased package

	defaultCfg := deps.Config.DefaultGenerationSettings

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		deps.Logger.Error("Failed to get user config from DB", zap.Error(err), zap.Int64("user_id", userID))
		// Use I18n for error message
		deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "myconfig_error_get_config")))
		// deps.Bot.Send(tgbotapi.NewMessage(chatID, "获取您的配置时出错，请稍后再试。"))
		return
	}

	// Determine current settings to display
	imgSize := defaultCfg.ImageSize
	infSteps := defaultCfg.NumInferenceSteps
	guidScale := defaultCfg.GuidanceScale
	numImages := defaultCfg.NumImages
	languageCode := deps.Config.DefaultLanguage // Start with default lang
	isLangDefault := true

	var currentSettingsMsgKey string
	if userCfg != nil { // User has custom config
		currentSettingsMsgKey = "myconfig_current_custom_settings"
		// Direct assignment, fields are no longer pointers
		imgSize = userCfg.ImageSize
		infSteps = userCfg.NumInferenceSteps
		guidScale = userCfg.GuidanceScale
		numImages = userCfg.NumImages                                 // Read user's num images directly
		languageCode = userCfg.Language                               // Check user's language preference directly
		isLangDefault = (languageCode == deps.Config.DefaultLanguage) // Update isLangDefault based on direct comparison

	} else {
		currentSettingsMsgKey = "myconfig_current_default_settings"
		// Assign defaults from config
		imgSize = defaultCfg.ImageSize
		infSteps = defaultCfg.NumInferenceSteps
		guidScale = defaultCfg.GuidanceScale
		numImages = defaultCfg.NumImages
		languageCode = deps.Config.DefaultLanguage
		isLangDefault = true
	}

	// Build the settings text using strings.Builder and i18n
	var settingsBuilder strings.Builder
	settingsBuilder.WriteString(deps.I18n.T(userLang, currentSettingsMsgKey))

	// Image Size
	settingsBuilder.WriteString(deps.I18n.T(userLang, "myconfig_setting_image_size", "value", imgSize))
	// Inference Steps
	settingsBuilder.WriteString(deps.I18n.T(userLang, "myconfig_setting_inf_steps", "value", strconv.Itoa(infSteps)))
	// Guidance Scale
	settingsBuilder.WriteString(deps.I18n.T(userLang, "myconfig_setting_guid_scale", "value", guidScale))
	// Number of Images
	// Convert int to string for the template value
	settingsBuilder.WriteString(deps.I18n.T(userLang, "myconfig_setting_num_images", "value", strconv.Itoa(numImages)))

	// Language Setting - Restore langName retrieval
	langName, langFound := deps.I18n.GetLanguageName(languageCode)
	if !langFound { // Fallback if lang code somehow invalid
		langName = languageCode
	}
	if isLangDefault {
		settingsBuilder.WriteString(deps.I18n.T(userLang, "myconfig_setting_language_default", "value", fmt.Sprintf("%s (%s)", langName, languageCode)))
	} else {
		settingsBuilder.WriteString(deps.I18n.T(userLang, "myconfig_setting_language", "value", fmt.Sprintf("%s (%s)", langName, languageCode)))
	}

	settingsText := settingsBuilder.String()

	// Create inline keyboard for modification using I18n
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "myconfig_button_set_image_size"), "config_set_imagesize")),     // "设置图片尺寸"
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "myconfig_button_set_inf_steps"), "config_set_infsteps")),       // "设置推理步数"
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "myconfig_button_set_guid_scale"), "config_set_guidscale")),     // "设置 Guidance Scale"
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "myconfig_button_set_num_images"), "config_set_numimages")),     // "设置生成数量"
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "config_callback_button_set_language"), "config_set_language")), // Add language button
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "myconfig_button_reset_defaults"), "config_reset_defaults")),    // "恢复默认设置"
	)

	reply := tgbotapi.NewMessage(chatID, settingsText)
	// Switch back to ModeMarkdown
	reply.ParseMode = tgbotapi.ModeMarkdown
	reply.ReplyMarkup = keyboard // Ensure pointer is used
	deps.Bot.Send(reply)
}

// Handles text input when user is expected to provide a config value
func HandleConfigUpdateInput(message *tgbotapi.Message, state *UserState, deps BotDeps) {
	userID := message.From.ID
	chatID := message.Chat.ID
	inputText := message.Text

	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// Replace sendGenericError with direct logging and sending
		deps.Logger.Error("Failed to get user config for update", zap.Error(err), zap.Int64("user_id", userID))
		userLang := getUserLanguagePreference(userID, deps)
		deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "error_generic")))
		deps.StateManager.ClearState(userID) // Clear state on error
		return
	}
	// Initialize if nil (using defaults from config)
	if userCfg == nil {
		defaultCfg := deps.Config.DefaultGenerationSettings
		userCfg = &st.UserGenerationConfig{
			UserID:            userID,
			ImageSize:         defaultCfg.ImageSize,
			NumInferenceSteps: defaultCfg.NumInferenceSteps,
			GuidanceScale:     defaultCfg.GuidanceScale,
			NumImages:         defaultCfg.NumImages,
			Language:          deps.Config.DefaultLanguage,
		}
		deps.Logger.Debug("Initialized new config for user during config update", zap.Int64("user_id", userID))
	}

	var updateErr error
	action := state.Action // e.g., "awaiting_config_infsteps"

	switch action {
	case "awaiting_config_infsteps":
		steps, err := strconv.Atoi(inputText)
		if err != nil || steps <= 0 || steps > 50 {
			// More specific error, ask user to retry
			// Use I18n for error message
			userLang := getUserLanguagePreference(userID, deps)
			deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "config_invalid_input_int_range", "min", 1, "max", 50)))
			// deps.Bot.Send(tgbotapi.NewMessage(chatID, "⚠️ 无效输入。请输入 1 到 50 之间的整数。"))
			return // Don't clear state, let user try again
		}
		// Assign value directly
		userCfg.NumInferenceSteps = steps
		// Fix SetUserGenerationConfig call signature
		updateErr = st.SetUserGenerationConfig(deps.DB, *userCfg)

	case "awaiting_config_guidscale":
		scale, err := strconv.ParseFloat(inputText, 64)
		if err != nil || scale < 0 || scale > 15 {
			// More specific error, ask user to retry
			userLang := getUserLanguagePreference(userID, deps)
			deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "config_invalid_input_float_range", "min", 0.0, "max", 15.0)))
			// deps.Bot.Send(tgbotapi.NewMessage(chatID, "⚠️ 无效输入。请输入 0 到 15 之间的数字 (例如 7.5)。"))
			return // Don't clear state
		}
		// Assign value directly
		userCfg.GuidanceScale = scale
		// Fix SetUserGenerationConfig call signature
		updateErr = st.SetUserGenerationConfig(deps.DB, *userCfg)

	case "awaiting_config_numimages":
		numImages, err := strconv.Atoi(inputText)
		// Validate the input (e.g., 1-10, adjust as needed)
		if err != nil || numImages <= 0 || numImages > 10 {
			userLang := getUserLanguagePreference(userID, deps)
			deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "config_invalid_input_int_range", "min", 1, "max", 10)))
			// deps.Bot.Send(tgbotapi.NewMessage(chatID, "⚠️ 无效输入。请输入 1 到 10 之间的整数。"))
			return // Don't clear state, let user try again
		}
		// Assign value directly
		userCfg.NumImages = numImages
		// Fix SetUserGenerationConfig call signature
		updateErr = st.SetUserGenerationConfig(deps.DB, *userCfg)

	default:
		deps.Logger.Warn("Received text input in unexpected config state", zap.String("action", action), zap.Int64("user_id", userID))
		// Use I18n
		userLang := getUserLanguagePreference(userID, deps)
		deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "unhandled_state_error")))
		// deps.Bot.Send(tgbotapi.NewMessage(chatID, "未知状态或操作"))
	}

	if updateErr != nil {
		// Replace sendGenericError with direct logging and sending
		deps.Logger.Error("Failed to set config value", zap.Error(updateErr), zap.Int64("user_id", userID), zap.String("action", action))
		userLang := getUserLanguagePreference(userID, deps)
		deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "error_generic")))
	} else {
		deps.Logger.Info("User config updated successfully", zap.Int64("user_id", userID), zap.String("action", action))
		// Use I18n for the success message, using the *current* user language
		userLang := getUserLanguagePreference(userID, deps)
		// Find the appropriate success message key based on action?
		// For now, let's use a generic update success message, or reuse the language update message?
		// Let's use the language update message key for now, although it's not ideal.
		// A better approach would be specific keys for each config update success.
		successMsgKey := "config_callback_lang_updated" // Reusing this for simplicity, ideally use a dedicated key
		// What params does this key expect? langName, langCode
		// We don't have these here easily. Let's define a new generic key.
		successMsgKey = "config_update_success" // Define this in JSON files
		deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, successMsgKey)))
		// Send a new message showing the updated config
		syntheticMsg := &tgbotapi.Message{
			From: message.From, // Use current message context
			Chat: message.Chat,
		}
		HandleMyConfigCommand(syntheticMsg, deps) // Call the function that SENDS the config message
	}
	deps.StateManager.ClearState(userID) // Clear state after successful update or unrecoverable error
}

// HandleAdminCallback handles admin-related callback queries for user management
func HandleAdminCallback(callbackQuery *tgbotapi.CallbackQuery, deps BotDeps) {
	userID := callbackQuery.From.ID
	var chatID int64
	var messageID int
	if callbackQuery.Message != nil {
		chatID = callbackQuery.Message.Chat.ID
		messageID = callbackQuery.Message.MessageID
	} else {
		deps.Logger.Error("Admin callback query message is nil", zap.Int64("user_id", userID), zap.String("data", callbackQuery.Data))
		answer := tgbotapi.NewCallback(callbackQuery.ID, deps.I18n.T(nil, "callback_error_nil_message"))
		deps.Bot.Request(answer)
		return
	}
	data := callbackQuery.Data
	userLang := getUserLanguagePreference(userID, deps)

	// Check if user is admin
	if !deps.Authorizer.IsAdmin(userID) {
		answer := tgbotapi.NewCallback(callbackQuery.ID, deps.I18n.T(userLang, "myconfig_command_admin_only"))
		deps.Bot.Request(answer)
		return
	}

	answer := tgbotapi.NewCallback(callbackQuery.ID, "")

	// Handle different admin actions
	if strings.HasPrefix(data, "admin_user_") {
		// Extract target user ID
		targetUserIDStr := strings.TrimPrefix(data, "admin_user_")
		targetUserID, err := strconv.ParseInt(targetUserIDStr, 10, 64)
		if err != nil {
			deps.Logger.Error("Failed to parse target user ID", zap.Error(err), zap.String("data", data))
			answer.Text = deps.I18n.T(userLang, "admin_invalid_user_id")
			deps.Bot.Request(answer)
			return
		}

		// Get current balance
		var currentBalance float64
		if deps.BalanceManager != nil {
			currentBalance = deps.BalanceManager.GetBalance(targetUserID)
		}

		// Show options for this user
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(
					fmt.Sprintf("💰 Set Balance (Current: %.2f)", currentBalance),
					fmt.Sprintf("admin_setbalance_%d", targetUserID),
				),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("⬅️ Back to User List", "admin_userlist"),
			),
		)

		msgText := fmt.Sprintf("👤 User: %d\n💰 Current Balance: %.2f\n\nSelect an action:", targetUserID, currentBalance)
		edit := tgbotapi.NewEditMessageText(chatID, messageID, msgText)
		edit.ReplyMarkup = &keyboard
		edit.ParseMode = tgbotapi.ModeMarkdown
		deps.Bot.Send(edit)
		deps.Bot.Request(answer)

	} else if strings.HasPrefix(data, "admin_setbalance_") {
		// Set state for balance input
		targetUserIDStr := strings.TrimPrefix(data, "admin_setbalance_")
		targetUserID, err := strconv.ParseInt(targetUserIDStr, 10, 64)
		if err != nil {
			deps.Logger.Error("Failed to parse target user ID for balance set", zap.Error(err), zap.String("data", data))
			answer.Text = deps.I18n.T(userLang, "admin_invalid_user_id")
			deps.Bot.Request(answer)
			return
		}

		// Set state to await balance input
		state := &UserState{
			UserID:        userID,
			ChatID:        chatID,
			MessageID:     messageID,
			Action:        fmt.Sprintf("awaiting_admin_balance_%d", targetUserID),
			SelectedLoras: []string{}, // Not used but required by struct
		}
		deps.StateManager.SetState(userID, state)

		// Create cancel keyboard
		cancelKeyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("❌ Cancel", "admin_cancel_balance_input"),
			),
		)

		promptText := fmt.Sprintf("Please enter the new balance for user %d:\n(Current balance: %.2f)", targetUserID, deps.BalanceManager.GetBalance(targetUserID))
		edit := tgbotapi.NewEditMessageText(chatID, messageID, promptText)
		edit.ReplyMarkup = &cancelKeyboard
		deps.Bot.Send(edit)
		answer.Text = "Enter new balance"
		deps.Bot.Request(answer)

	} else if data == "admin_userlist" {
		// Show user list again
		syntheticMsg := &tgbotapi.Message{
			MessageID: messageID,
			From:      callbackQuery.From,
			Chat:      callbackQuery.Message.Chat,
		}
		HandleSetCommand(syntheticMsg, deps)
		deps.Bot.Request(answer)

	} else if data == "admin_cancel_balance_input" {
		// Cancel balance input
		deps.StateManager.ClearState(userID)
		answer.Text = "Cancelled"
		deps.Bot.Request(answer)
		// Go back to user list
		syntheticMsg := &tgbotapi.Message{
			MessageID: messageID,
			From:      callbackQuery.From,
			Chat:      callbackQuery.Message.Chat,
		}
		HandleSetCommand(syntheticMsg, deps)
	}
}
