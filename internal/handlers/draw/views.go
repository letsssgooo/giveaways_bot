package draw

import (
	"context"
	"fmt"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

const pageSize = 10

func bounds(page, length int) (int, int, int) {
	page = min(page, max(0, (length-1)/pageSize))
	start := page * pageSize
	return page, start, min(start+pageSize, length)
}
func (h *Handler) list(ctx context.Context, r request, page int) error {
	values, err := h.service.List(ctx, r.actorID)
	if err != nil {
		return err
	}
	page, start, end := bounds(page, len(values))
	markup := keyboard()
	for _, value := range values[start:end] {
		label := title(value)
		switch {
		case value.ApprovedAt != nil:
			label += " · утвержден"
		case value.PerformedAt != nil:
			label += " · к утверждению"
		default:
			label += " · запланирован"
		}
		markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{button(label, fmt.Sprintf("view:%d", value.ID))})
	}
	addPages(markup, "list:", page, end, len(values))
	markup.InlineKeyboard = append(markup.InlineKeyboard, back().InlineKeyboard...)
	text := "Розыгрыши. Выберите результат для просмотра и утверждения."
	if len(values) == 0 {
		text = "Доступных розыгрышей пока нет. Расписание и призы задаются в админке."
	}
	return h.reply(ctx, r, text, markup)
}
func (h *Handler) show(ctx context.Context, r request, result *entity.Result) error {
	var buttons []models.InlineKeyboardButton
	if result.PerformedAt != nil {
		buttons = append(buttons, button("Победители и резерв", fmt.Sprintf("results:%d:0", result.ID)), button("Состав участников", fmt.Sprintf("participants:%d:0", result.ID)))
		if result.ApprovedAt == nil {
			buttons = append(buttons, button("Утвердить результат", fmt.Sprintf("approve:%d", result.ID)))
		}
	} else {
		buttons = append(buttons, button("Обновить", fmt.Sprintf("view:%d", result.ID)))
	}
	buttons = append(buttons, button("К розыгрышам", "list:0"))
	return h.reply(ctx, r, summaryText(result.Summary), keyboard(buttons...))
}
func (h *Handler) details(ctx context.Context, r request, result *entity.Result, kind string, page int) error {
	var lines []string
	header := "Состав участников"
	if kind == "participants" {
		for _, p := range result.Participants {
			lines = append(lines, fmt.Sprintf("Telegram ID %d — вес %d", p.TelegramID, p.Weight))
		}
	} else {
		header = "Победители и резерв"
		for _, winner := range result.Winners {
			lines = append(lines, fmt.Sprintf("Победитель %d: Telegram ID %d\n%s — %d шт. (место %d)", winner.Position, winner.TelegramID, winner.PrizeName, winner.UnitsPerWinner, winner.PlaceNumber))
		}
		for _, reserve := range result.Reserves {
			lines = append(lines, fmt.Sprintf("Резерв %d: Telegram ID %d", reserve.Position, reserve.TelegramID))
		}
	}
	page, start, end := bounds(page, len(lines))
	text := title(result.Summary) + "\n" + header
	if len(lines) == 0 {
		text += "\nСписок пуст."
	}
	for _, line := range lines[start:end] {
		text += "\n\n" + line
	}
	markup := keyboard()
	addPages(markup, fmt.Sprintf("%s:%d:", kind, result.ID), page, end, len(lines))
	markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{button("К результату", fmt.Sprintf("view:%d", result.ID))})
	return h.reply(ctx, r, text, markup)
}
func addPages(markup *models.InlineKeyboardMarkup, prefix string, page, end, total int) {
	var navigation []models.InlineKeyboardButton
	if page > 0 {
		navigation = append(navigation, button("← Назад", fmt.Sprintf("%s%d", prefix, page-1)))
	}
	if end < total {
		navigation = append(navigation, button("Далее →", fmt.Sprintf("%s%d", prefix, page+1)))
	}
	if len(navigation) != 0 {
		markup.InlineKeyboard = append(markup.InlineKeyboard, navigation)
	}
}
func (h *Handler) reply(ctx context.Context, r request, text string, markup *models.InlineKeyboardMarkup) error {
	var parts []string
	start, size := 0, 0
	for i, char := range text {
		length := utf16.RuneLen(char)
		if size+length > protocol.MaxMessageLength {
			parts = append(parts, text[start:i])
			start, size = i, 0
		}
		size += length
	}
	parts = append(parts, text[start:])
	for i, part := range parts {
		var partMarkup *models.InlineKeyboardMarkup
		if i == len(parts)-1 {
			partMarkup = markup
		}
		payload := struct {
			ChatID int64                        `json:"chat_id"`
			Text   string                       `json:"text"`
			Markup *models.InlineKeyboardMarkup `json:"reply_markup,omitempty"`
		}{r.actorID, part, partMarkup}
		if err := h.enqueue(ctx, r, protocol.MessageKindText, fmt.Sprintf("reply:%d", i), payload); err != nil {
			return err
		}
	}
	return nil
}
