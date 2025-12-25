package bot

import (
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

// Helper to send or edit the Lora selection keyboard
func SendLoraSelectionKeyboard(chatID int64, messageID int, state *UserState, deps BotDeps, edit bool) {
	// Get LoRAs visible to this user
	visibleLoras := GetUserVisibleLoras(state.UserID, deps)
	userLang := getUserLanguagePreference(state.UserID, deps)

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
				// Use I18n for checkmark
				buttonText = deps.I18n.T(userLang, "button_checkmark") + " " + lora.Name
				// buttonText = "✅ " + lora.Name
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
		// Use I18n
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "lora_selection_keyboard_none_available"), "lora_noop")))
		// rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("无可用 LoRA 风格", "lora_noop")))
	}

	// --- Remove Base LoRA selection from this keyboard ---
	// Base LoRAs are selected in the next step (SendBaseLoraSelectionKeyboard)

	// --- Action Buttons: Done with Standard LoRAs / Cancel ---
	// Show "Next Step" button only if at least one standard LoRA is available
	if len(visibleLoras) > 0 {
		nextButtonText := deps.I18n.T(userLang, "lora_selection_keyboard_next_button")
		// nextButtonText := "➡️ 下一步: 选择 Base LoRA"
		if len(state.SelectedLoras) == 0 {
			// Optional: Disable next step button if none selected? Or rely on callback check.
			// For now, allow clicking, callback handler will check.
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(nextButtonText, "lora_standard_done"), // Corrected callback data
			tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "lora_selection_keyboard_cancel_button"), "lora_cancel"),
			// tgbotapi.NewInlineKeyboardButtonData("❌ 取消", "lora_cancel"),
		))
	} else {
		// Only show Cancel if no LoRAs are available
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "lora_selection_keyboard_cancel_button"), "lora_cancel"),
			// tgbotapi.NewInlineKeyboardButtonData("❌ 取消", "lora_cancel"),
		))
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	// Construct the prompt text using strings.Builder, use I18n
	var loraPromptBuilder strings.Builder
	loraPromptBuilder.WriteString(deps.I18n.T(userLang, "lora_selection_keyboard_prompt"))
	// loraPromptBuilder.WriteString("请选择您想使用的标准 LoRA 风格")
	if len(state.SelectedLoras) > 0 {
		// Simple join, backticks should work in ModeMarkdown
		loraPromptBuilder.WriteString(deps.I18n.T(userLang, "lora_selection_keyboard_selected", "selection", fmt.Sprintf("`%s`", strings.Join(state.SelectedLoras, "`, `"))))
		// loraPromptBuilder.WriteString(fmt.Sprintf(" (已选: `%s`)", strings.Join(state.SelectedLoras, "`, `")))
	}

	// Escape markdown in the user's caption before embedding
	escapedCaption := state.OriginalCaption
	// Escape backticks first, then other characters
	escapedCaption = strings.ReplaceAll(escapedCaption, "`", "\\`") // Escape backticks
	escapedCaption = strings.ReplaceAll(escapedCaption, "*", "\\*") // Escape asterisks
	escapedCaption = strings.ReplaceAll(escapedCaption, "_", "\\_") // Escape underscores

	loraPromptBuilder.WriteString(deps.I18n.T(userLang, "lora_selection_keyboard_prompt_suffix", "prompt", escapedCaption))
	// loraPromptBuilder.WriteString(":\nPrompt: ```\n")
	// loraPromptBuilder.WriteString(escapedCaption) // Use escaped version
	// loraPromptBuilder.WriteString("\n```")
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

	userLang := getUserLanguagePreference(state.UserID, deps)
	var rows [][]tgbotapi.InlineKeyboardButton
	maxButtonsPerRow := 2
	promptBuilder := strings.Builder{}

	// Build prompt text using i18n
	promptBuilder.WriteString(deps.I18n.T(userLang, "base_lora_selection_keyboard_selected_standard", "selection", fmt.Sprintf("`%s`", strings.Join(state.SelectedLoras, "`, `"))))
	maxLoras := deps.Config.APIEndpoints.MaxLoras
	if maxLoras <= 0 {
		maxLoras = 2
	}
	promptBuilder.WriteString(deps.I18n.T(userLang, "base_lora_selection_keyboard_prompt", "max", maxLoras))
	if len(state.SelectedBaseLoras) > 0 {
		promptBuilder.WriteString(deps.I18n.T(userLang, "base_lora_selection_keyboard_current_base", "name", strings.Join(state.SelectedBaseLoras, ", ")))
	}

	// --- Base LoRA Buttons --- // Use I18n for button text
	currentRow := []tgbotapi.InlineKeyboardButton{}
	selectedBaseSet := make(map[string]struct{}, len(state.SelectedBaseLoras))
	for _, name := range state.SelectedBaseLoras {
		selectedBaseSet[name] = struct{}{}
	}
	if len(visibleBaseLoras) > 0 {
		for _, lora := range visibleBaseLoras {
			buttonText := lora.Name
			if _, ok := selectedBaseSet[lora.Name]; ok {
				buttonText = deps.I18n.T(userLang, "button_checkmark") + " " + lora.Name // Mark selected
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
		// No base loras available/visible for selection - use i18n
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "base_lora_selection_keyboard_none_available"), "lora_noop")))
	}

	// --- Action Buttons --- // Use i18n for button text
	skipButtonText := deps.I18n.T(userLang, "base_lora_selection_keyboard_skip_button")
	if len(state.SelectedBaseLoras) == 0 { // User hasn't selected one yet
		// Show skip button, but check if they have already *explicitly* skipped (though state doesn't track that directly)
		// Let's assume if name is empty, they either haven't chosen or have deselected/skipped.
		// Maybe change text if deselected? For now, keep it simple: Show Skip or Deselect.
		skipButtonText = deps.I18n.T(userLang, "base_lora_selection_keyboard_skip_button")
	} else { // User has selected one
		skipButtonText = deps.I18n.T(userLang, "base_lora_selection_keyboard_deselect_button")
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(skipButtonText, "base_lora_skip"), // Callback remains the same
	))
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "base_lora_selection_keyboard_confirm_button"), "lora_confirm_generate"),
		tgbotapi.NewInlineKeyboardButtonData(deps.I18n.T(userLang, "base_lora_selection_keyboard_cancel_button"), "base_lora_cancel"),
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
