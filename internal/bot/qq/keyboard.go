package qq

import (
	"strings"

	"reasonix/internal/bot"
)

func qqKeyboardContent(keyboard *bot.InlineKeyboard, chatType bot.ChatType) map[string]any {
	rows := make([]map[string]any, 0, len(keyboard.Rows))
	for _, row := range keyboard.Rows {
		buttons := make([]map[string]any, 0, len(row.Buttons))
		for _, btn := range row.Buttons {
			style := btn.Style
			if style == 2 {
				style = 3
			}
			buttons = append(buttons, map[string]any{
				"id": strings.TrimSpace(btn.ID),
				"render_data": map[string]any{
					"label": btn.Label,
					"style": style,
				},
				"action": map[string]any{
					"type":       2,
					"data":       btn.CallbackID,
					"permission": map[string]int{"type": 2},
					"enter":      chatType == bot.ChatDM,
				},
			})
		}
		rows = append(rows, map[string]any{"buttons": buttons})
	}
	return map[string]any{"content": map[string]any{"rows": rows}}
}
