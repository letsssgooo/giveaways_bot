package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/notifications"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

const pageSize = 10

var moscow = time.FixedZone("Europe/Moscow", 3*60*60)

func button(text, action string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: "broadcast:" + action}
}
func keyboard(buttons ...models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	markup := &models.InlineKeyboardMarkup{}
	for _, b := range buttons {
		markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{b})
	}
	return markup
}
func back() *models.InlineKeyboardMarkup {
	return keyboard(button("К рассылкам", "list:0"), models.InlineKeyboardButton{Text: "В админку", CallbackData: "admin:home"})
}
func bounds(page, length int) (int, int, int) {
	page = min(page, max(0, (length-1)/pageSize))
	start := page * pageSize
	return page, start, min(start+pageSize, length)
}
func addPages(markup *models.InlineKeyboardMarkup, prefix string, page, end, total int) {
	var row []models.InlineKeyboardButton
	if page > 0 {
		row = append(row, button("← Назад", fmt.Sprintf("%s%d", prefix, page-1)))
	}
	if end < total {
		row = append(row, button("Далее →", fmt.Sprintf("%s%d", prefix, page+1)))
	}
	if len(row) > 0 {
		markup.InlineKeyboard = append(markup.InlineKeyboard, row)
	}
}
func (h *Handler) audiences(ctx context.Context, r request) error {
	access, err := h.access(ctx, r)
	if err != nil {
		return err
	}
	var buttons []models.InlineKeyboardButton
	if access.Organizer {
		buttons = append(buttons, button("Все пользователи бота", "audience:all"), button("Список Telegram ID", "audience:users"))
	}
	buttons = append(buttons, button("Участники партнера", "audience:partner"), button("К рассылкам", "list:0"))
	return h.reply(ctx, r, "Выберите аудиторию рассылки. Участники партнера — пользователи, получавшие его промокод или регистрировавшиеся в его розыгрыше.", keyboard(buttons...))
}
func (h *Handler) partners(ctx context.Context, r request, page int) error {
	partners, err := h.admin.Partners(ctx, r.actorID)
	if err != nil {
		return err
	}
	page, start, end := bounds(page, len(partners))
	markup := keyboard()
	for _, partner := range partners[start:end] {
		markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{button(partner.Name, fmt.Sprintf("partner:%d", partner.ID))})
	}
	addPages(markup, "partners:", page, end, len(partners))
	markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{button("К выбору аудитории", "new")})
	text := "Выберите партнера, участникам которого предназначена рассылка."
	if len(partners) == 0 {
		text = "Доступных партнеров пока нет."
	}
	return h.reply(ctx, r, text, markup)
}
func status(value entity.Broadcast) string {
	switch value.Status {
	case "draft":
		return "черновик"
	case "cancelled":
		return "отменена"
	case "queued":
		if value.Pending == 0 {
			return "завершена"
		}
		return "отправка"
	default:
		return value.Status
	}
}
func audience(value entity.Audience, partnerName string) string {
	switch value.Kind {
	case "all":
		return "все пользователи бота"
	case "partner":
		if partnerName != "" {
			return fmt.Sprintf("участники партнера «%s»", partnerName)
		}
		return fmt.Sprintf("участники партнера #%d", value.PartnerID)
	case "users":
		return fmt.Sprintf("список Telegram ID (%d)", len(value.TelegramIDs))
	default:
		return value.Kind
	}
}
func (h *Handler) list(ctx context.Context, r request, page int) error {
	values, err := h.service.List(ctx, r.actorID)
	if err != nil {
		return err
	}
	page, start, end := bounds(page, len(values))
	markup := keyboard(button("Создать рассылку", "new"))
	for _, value := range values[start:end] {
		label := fmt.Sprintf("#%d · %s · %s", value.ID, value.CreatedAt.In(moscow).Format("02.01 15:04"), status(value))
		markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{button(label, fmt.Sprintf("view:%d", value.ID))})
	}
	addPages(markup, "list:", page, end, len(values))
	markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{{Text: "В админку", CallbackData: "admin:home"}})
	text := "Рассылки. Выберите черновик или отправленную рассылку для просмотра."
	if len(values) == 0 {
		text = "Рассылок пока нет. Создайте черновик, выберите аудиторию и проверьте сообщение перед отправкой."
	}
	return h.reply(ctx, r, text, markup)
}
func (h *Handler) show(ctx context.Context, r request, value *entity.Broadcast) error {
	var partnerName string
	if value.Audience.Kind == entity.AudiencePartner {
		partners, err := h.admin.Partners(ctx, r.actorID)
		if err != nil {
			return err
		}
		for _, partner := range partners {
			if partner.ID == value.Audience.PartnerID {
				partnerName = partner.Name
				break
			}
		}
	}
	// Предпросмотр отдельным сообщением сохраняет точный текст даже при длине 4096.
	if err := h.message(ctx, r, "preview", value.Text, nil); err != nil {
		return err
	}
	text := fmt.Sprintf("Рассылка #%d\nАудитория: %s\nПолучателей: %d\nСтатус: %s", value.ID, audience(value.Audience, partnerName), value.Total, status(*value))
	var buttons []models.InlineKeyboardButton
	switch value.Status {
	case "draft":
		payload, err := json.Marshal(flow{Audience: value.Audience, BroadcastID: value.ID, Nonce: r.updateID})
		if err != nil {
			return err
		}
		if err := h.dialogs.Save(ctx, r.userID, "broadcast:confirm", payload, 30*time.Minute); err != nil {
			return err
		}
		text += "\n\nВыше — точный текст сообщения. Список получателей зафиксирован при подготовке. Отправить рассылку?"
		buttons = append(buttons, button("Подтвердить отправку", fmt.Sprintf("send:%d:%d", value.ID, r.updateID)), button("Отменить рассылку", fmt.Sprintf("cancel:%d", value.ID)))
	case "queued":
		text += fmt.Sprintf("\nОжидают отправки: %d\nДоставлено: %d\nНе доставлено: %d", value.Pending, value.Sent, value.Failed)
		buttons = append(buttons, button("Обновить состояние", fmt.Sprintf("view:%d", value.ID)))
	}
	buttons = append(buttons, button("К рассылкам", "list:0"))
	return h.reply(ctx, r, text, keyboard(buttons...))
}
func (h *Handler) message(ctx context.Context, r request, suffix, text string, markup *models.InlineKeyboardMarkup) error {
	payload := struct {
		ChatID int64                        `json:"chat_id"`
		Text   string                       `json:"text"`
		Markup *models.InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	}{r.actorID, text, markup}
	return h.enqueue(ctx, r, protocol.MessageKindText, suffix, payload)
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
		if err := h.message(ctx, r, fmt.Sprintf("reply:%d", i), part, partMarkup); err != nil {
			return err
		}
	}
	return nil
}
