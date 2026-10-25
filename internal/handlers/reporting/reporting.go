// Package reporting содержит просмотр статистики и запрос выгрузок в Telegram.
package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/reporting"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type Service interface {
	Partners(context.Context, int64) ([]entity.Partner, error)
	Stats(context.Context, int64, int64) (entity.Stats, error)
	Authorize(context.Context, int64, int64) error
}
type Outbox interface {
	Enqueue(context.Context, int64, int64, string, json.RawMessage, string) error
}
type Dialogs interface {
	Clear(context.Context, int64) error
}
type Handler struct {
	service Service
	users   *userservice.Service
	dialogs Dialogs
	outbox  Outbox
}

func New(service Service, users *userservice.Service, dialogs Dialogs, outbox Outbox) *Handler {
	return &Handler{service, users, dialogs, outbox}
}

type request struct{ userID, actorID, updateID int64 }

func (h *Handler) Handle(ctx context.Context, update *models.Update) (bool, error) {
	if update == nil || update.ID < 0 || update.CallbackQuery == nil {
		return false, nil
	}
	callback := update.CallbackQuery
	if !strings.HasPrefix(callback.Data, "report:") || callback.ID == "" {
		return false, nil
	}
	var chat models.Chat
	inaccessible := callback.Message.Type == models.MaybeInaccessibleMessageTypeInaccessibleMessage
	if inaccessible && callback.Message.InaccessibleMessage != nil {
		chat = callback.Message.InaccessibleMessage.Chat
	} else if callback.Message.Type == models.MaybeInaccessibleMessageTypeMessage && callback.Message.Message != nil {
		chat = callback.Message.Message.Chat
	}
	if callback.From.IsBot || callback.From.ID <= 0 || chat.Type != models.ChatTypePrivate || chat.ID != callback.From.ID {
		return true, nil
	}
	user, err := h.users.Ensure(ctx, callback.From.ID, chat.ID)
	if err != nil {
		return true, err
	}
	r := request{user.ID, callback.From.ID, int64(update.ID)}
	if err := h.enqueue(ctx, r, protocol.MessageKindCallbackAnswer, "callback", map[string]string{"callback_query_id": callback.ID}); err != nil {
		return true, err
	}
	if err := h.dialogs.Clear(ctx, r.userID); err != nil {
		return true, err
	}
	if inaccessible {
		return true, h.reply(ctx, r, "Кнопка устарела. Откройте /admin.", back())
	}
	err = h.action(ctx, r, strings.Split(callback.Data, ":"))
	for _, expected := range []error{promotionadminentity.ErrForbidden, promotionadminentity.ErrInvalid, promotionadminentity.ErrNotFound} {
		if errors.Is(err, expected) {
			return true, h.reply(ctx, r, err.Error()+". Откройте /admin.", back())
		}
	}
	return true, err
}

func (h *Handler) action(ctx context.Context, r request, parts []string) error {
	if len(parts) == 3 && parts[1] == "scope" {
		page, err := number(parts[2])
		if err != nil {
			return err
		}
		return h.scopes(ctx, r, int(page))
	}
	if len(parts) == 4 && parts[1] == "export" {
		kind := parts[2]
		if kind != "participants" && kind != "winners" {
			return promotionadminentity.ErrInvalid
		}
		partner, err := number(parts[3])
		if err != nil {
			return err
		}
		if err := h.service.Authorize(ctx, r.actorID, partner); err != nil {
			return err
		}
		if err := h.reply(ctx, r, "Выгрузка поставлена в очередь и придет отдельным CSV-файлом. Данные будут актуальны на момент формирования.", keyboard(button("К статистике", fmt.Sprintf("home:%d", partner)))); err != nil {
			return err
		}
		return h.enqueue(ctx, r, protocol.MessageKindReport, "export", protocol.ReportRequest{ActorID: r.actorID, ChatID: r.actorID, PartnerID: partner, Kind: kind})
	}
	if len(parts) < 3 || len(parts) > 4 {
		return promotionadminentity.ErrInvalid
	}
	partner, err := number(parts[2])
	if err != nil {
		return err
	}
	if parts[1] == "home" && len(parts) == 3 {
		return h.home(ctx, r, partner)
	}
	if len(parts) != 4 {
		return promotionadminentity.ErrInvalid
	}
	page, err := number(parts[3])
	if err != nil {
		return err
	}
	switch parts[1] {
	case "partners", "days", "users":
		return h.details(ctx, r, partner, parts[1], int(page))
	default:
		return promotionadminentity.ErrInvalid
	}
}
func number(value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, promotionadminentity.ErrInvalid
	}
	return n, nil
}
func button(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: "report:" + data}
}
func keyboard(buttons ...models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	markup := &models.InlineKeyboardMarkup{}
	for _, b := range buttons {
		markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{b})
	}
	return markup
}
func back() *models.InlineKeyboardMarkup {
	return keyboard(models.InlineKeyboardButton{Text: "В админку", CallbackData: "admin:home"})
}
func (h *Handler) enqueue(ctx context.Context, r request, kind, suffix string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return h.outbox.Enqueue(ctx, r.userID, r.updateID, kind, encoded, fmt.Sprintf("telegram:update:%d:report:%s", r.updateID, suffix))
}
func (h *Handler) reply(ctx context.Context, r request, text string, markup *models.InlineKeyboardMarkup) error {
	var parts []string
	start, size := 0, 0
	for i, char := range text {
		n := utf16.RuneLen(char)
		if size+n > protocol.MaxMessageLength {
			parts = append(parts, text[start:i])
			start, size = i, 0
		}
		size += n
	}
	parts = append(parts, text[start:])
	for i, part := range parts {
		var kb *models.InlineKeyboardMarkup
		if i == len(parts)-1 {
			kb = markup
		}
		payload := struct {
			ChatID int64                        `json:"chat_id"`
			Text   string                       `json:"text"`
			Markup *models.InlineKeyboardMarkup `json:"reply_markup,omitempty"`
		}{r.actorID, part, kb}
		if err := h.enqueue(ctx, r, protocol.MessageKindText, fmt.Sprintf("reply:%d", i), payload); err != nil {
			return err
		}
	}
	return nil
}
