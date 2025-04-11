package bot

import (
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// 为选择 LoRA 创建键盘
func CreateLoraSelectionKeyboard(loras []LoraConfig) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton
	var currentRow []tgbotapi.InlineKeyboardButton

	for i, lora := range loras {
		// Callback data 格式: "select_lora:<lora_id>"
		button := tgbotapi.NewInlineKeyboardButtonData(lora.Name, fmt.Sprintf("select_lora:%s", lora.ID))
		currentRow = append(currentRow, button)

		// 每行放 2 个按钮
		if (i+1)%2 == 0 || i == len(loras)-1 {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(currentRow...))
			currentRow = []tgbotapi.InlineKeyboardButton{}
		}
	}
	// 添加一个“完成选择”按钮
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ 完成选择并生成", "lora_done"),
	))

	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

// 为编辑/确认 Caption 创建键盘
func CreateCaptionActionKeyboard(originalCaption string) tgbotapi.InlineKeyboardMarkup {
	// Callback data 格式: "caption_action:<action>"
	// action 可以是 "confirm", "edit"
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ 使用此描述", "caption_action:confirm"),
			// tgbotapi.NewInlineKeyboardButtonData("✏️ 编辑描述", "caption_action:edit"), // 编辑功能实现较复杂，先只做确认
		),
	)
	return keyboard
}
