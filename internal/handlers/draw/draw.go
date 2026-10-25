// Package draw содержит просмотр и утверждение результатов розыгрышей в Telegram.
package draw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type Service interface {
	List(context.Context, int64) ([]entity.Summary, error)
	View(context.Context, int64, int64) (*entity.Result, error)
	Approve(context.Context, int64, int64) (*entity.Result, error)
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
	return &Handler{service: service, users: users, dialogs: dialogs, outbox: outbox}
}

type request struct{ userID, actorID, updateID int64 }

// Handle обрабатывает свои кнопки только в личном чате. Ответы и утверждение
// сохраняются в общей транзакции входящего Telegram update.
func (h *Handler) Handle(ctx context.Context, update *models.Update) (bool, error) {
	if update == nil || update.ID < 0 || update.CallbackQuery == nil {
		return false, nil
	}
	callback := update.CallbackQuery
	if !strings.HasPrefix(callback.Data, "draw:") || callback.ID == "" {
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
	for _, expected := range []error{promotionadminentity.ErrForbidden, promotionadminentity.ErrInvalid, promotionadminentity.ErrNotFound, promotionadminentity.ErrConflict, entity.ErrNotReady} {
		if errors.Is(err, expected) {
			return true, h.reply(ctx, r, err.Error()+". Откройте /admin.", back())
		}
	}
	return true, err
}

func (h *Handler) action(ctx context.Context, r request, parts []string) error {
	if len(parts) == 3 && parts[1] == "list" {
		page, err := pageNumber(parts[2])
		if err != nil {
			return err
		}
		return h.list(ctx, r, page)
	}
	if len(parts) != 3 && len(parts) != 4 {
		return promotionadminentity.ErrInvalid
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return promotionadminentity.ErrInvalid
	}
	if len(parts) == 4 && (parts[1] == "participants" || parts[1] == "results") {
		page, err := pageNumber(parts[3])
		if err != nil {
			return err
		}
		result, err := h.service.View(ctx, r.actorID, id)
		if err != nil {
			return err
		}
		return h.details(ctx, r, result, parts[1], page)
	}
	if len(parts) != 3 {
		return promotionadminentity.ErrInvalid
	}
	switch parts[1] {
	case "view", "approve":
		result, err := h.service.View(ctx, r.actorID, id)
		if err != nil {
			return err
		}
		if parts[1] == "approve" && result.PerformedAt != nil && result.ApprovedAt == nil {
			return h.reply(ctx, r, summaryText(result.Summary)+"\n\nУтвердить этот результат? После утверждения он станет доступен для уведомления победителей.", keyboard(
				button("Подтвердить утверждение", fmt.Sprintf("confirm:%d", id)), button("Назад к результату", fmt.Sprintf("view:%d", id))))
		}
		return h.show(ctx, r, result)
	case "confirm":
		result, err := h.service.Approve(ctx, r.actorID, id)
		if err != nil {
			return err
		}
		return h.show(ctx, r, result)
	default:
		return promotionadminentity.ErrInvalid
	}
}
func pageNumber(text string) (int, error) {
	page, err := strconv.Atoi(text)
	if err != nil || page < 0 {
		return 0, promotionadminentity.ErrInvalid
	}
	return page, nil
}
func button(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: "draw:" + data}
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

var moscow = time.FixedZone("Europe/Moscow", 3*60*60)

func title(value entity.Summary) string {
	if value.Kind == "final" {
		return "Финальный розыгрыш"
	}
	return value.PartnerName + " · " + value.Date.Format("02.01.2006")
}
func summaryText(value entity.Summary) string {
	text := fmt.Sprintf("%s\nНазначен: %s МСК", title(value), value.ScheduledAt.In(moscow).Format("02.01.2006 15:04"))
	if value.PerformedAt == nil {
		return text + "\nСтатус: ожидает проведения."
	}
	text += fmt.Sprintf("\nПроведен: %s МСК\nУчастников: %d\nПобедителей: %d из %d\nРезервистов: %d",
		value.PerformedAt.In(moscow).Format("02.01.2006 15:04:05"), value.ParticipantCount, value.WinnerCount, value.PlacesCount, value.ReserveCount)
	if value.WinnerCount < value.PlacesCount {
		text += "\nУчастников не хватило на все призовые места. Повторный выбор не выполняется."
	}
	if value.ApprovedAt != nil {
		return text + "\nУтвержден: " + value.ApprovedAt.In(moscow).Format("02.01.2006 15:04") + " МСК."
	}
	return text + "\nСтатус: ожидает утверждения. Победителям уведомления не отправлялись."
}
func (h *Handler) enqueue(ctx context.Context, r request, kind, suffix string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return h.outbox.Enqueue(ctx, r.userID, r.updateID, kind, encoded, fmt.Sprintf("telegram:update:%d:draw:%s", r.updateID, suffix))
}
