// Package notifications содержит команды подготовки рассылок и просмотра состояния отправки.
package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/notifications"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type Service interface {
	Create(context.Context, int64, int64, entity.Audience, string) (*entity.Broadcast, error)
	View(context.Context, int64, int64) (*entity.Broadcast, error)
	List(context.Context, int64) ([]entity.Broadcast, error)
	Send(context.Context, int64, int64) (*entity.Broadcast, error)
	Cancel(context.Context, int64, int64) (*entity.Broadcast, error)
}

type Admin interface {
	Access(context.Context, int64) (promotionadminentity.Access, error)
	Partners(context.Context, int64) ([]promotionadminentity.PartnerConfig, error)
	AuthorizePartner(context.Context, int64, int64) error
}

type Dialogs interface {
	Load(context.Context, int64) (*dialogs.Dialog, error)
	Save(context.Context, int64, string, json.RawMessage, time.Duration) error
	Clear(context.Context, int64) error
}

type Outbox interface {
	Enqueue(context.Context, int64, int64, string, json.RawMessage, string) error
}

type Handler struct {
	service  Service
	admin    Admin
	users    *userservice.Service
	dialogs  Dialogs
	outbox   Outbox
	username string
}

func New(service Service, admin Admin, users *userservice.Service, dialogs Dialogs, outbox Outbox, username string) *Handler {
	return &Handler{service: service, admin: admin, users: users, dialogs: dialogs, outbox: outbox, username: strings.TrimPrefix(username, "@")}
}

type request struct{ userID, actorID, updateID int64 }
type flow struct {
	Audience    entity.Audience `json:"audience"`
	BroadcastID int64           `json:"broadcast_id,omitempty"`
	Nonce       int64           `json:"nonce,omitempty"`
}

// Handle готовит черновик и ставит рассылку в очередь только после отдельного
// подтверждения. Диалог, черновик и ответы сохраняются в транзакции update.
func (h *Handler) Handle(ctx context.Context, update *models.Update) (bool, error) {
	if update == nil || update.ID < 0 {
		return false, nil
	}
	if update.CallbackQuery != nil {
		return h.callback(ctx, update)
	}
	message := update.Message
	if message == nil || !private(message.From, message.Chat) {
		return false, nil
	}
	command, elsewhere := h.command(message.Text)
	if elsewhere {
		return false, nil
	}
	user, err := h.users.Ensure(ctx, message.From.ID, message.Chat.ID)
	if err != nil {
		return false, err
	}
	dialog, err := h.dialogs.Load(ctx, user.ID)
	if err != nil {
		return false, err
	}
	if dialog == nil || !strings.HasPrefix(dialog.State, "broadcast:") {
		return false, nil
	}
	r := request{user.ID, message.From.ID, update.ID}
	if command != "" {
		if err := h.dialogs.Clear(ctx, r.userID); err != nil {
			return true, err
		}
		if command == protocol.CommandCancel {
			return true, h.reply(ctx, r, "Ввод и подтверждение отправки отменены. Сохраненные черновики доступны в списке рассылок.", back())
		}
		return false, nil
	}
	if _, err := h.access(ctx, r); err != nil {
		return true, h.respondError(ctx, r, err)
	}
	var f flow
	if err := json.Unmarshal(dialog.Payload, &f); err != nil {
		return true, err
	}
	return true, h.respondError(ctx, r, h.input(ctx, r, dialog.State, f, message.Text))
}

func (h *Handler) callback(ctx context.Context, update *models.Update) (bool, error) {
	callback := update.CallbackQuery
	own := strings.HasPrefix(callback.Data, "broadcast:")
	var chat models.Chat
	inaccessible := callback.Message.Type == models.MaybeInaccessibleMessageTypeInaccessibleMessage
	if inaccessible && callback.Message.InaccessibleMessage != nil {
		chat = callback.Message.InaccessibleMessage.Chat
	} else if callback.Message.Type == models.MaybeInaccessibleMessageTypeMessage && callback.Message.Message != nil {
		chat = callback.Message.Message.Chat
	}
	if !private(&callback.From, chat) || callback.ID == "" {
		return own, nil
	}
	user, err := h.users.Ensure(ctx, callback.From.ID, chat.ID)
	if err != nil {
		return own, err
	}
	if !own {
		dialog, err := h.dialogs.Load(ctx, user.ID)
		if err != nil {
			return false, err
		}
		if dialog != nil && strings.HasPrefix(dialog.State, "broadcast:") {
			return false, h.dialogs.Clear(ctx, user.ID)
		}
		return false, nil
	}
	r := request{user.ID, callback.From.ID, update.ID}
	if err := h.enqueue(ctx, r, protocol.MessageKindCallbackAnswer, "callback", map[string]string{"callback_query_id": callback.ID}); err != nil {
		return true, err
	}
	if !strings.HasPrefix(callback.Data, "broadcast:send:") {
		if err := h.dialogs.Clear(ctx, r.userID); err != nil {
			return true, err
		}
	}
	if inaccessible {
		return true, h.reply(ctx, r, "Кнопка устарела. Откройте /admin.", back())
	}
	if _, err := h.access(ctx, r); err != nil {
		return true, h.respondError(ctx, r, err)
	}
	return true, h.respondError(ctx, r, h.action(ctx, r, strings.Split(callback.Data, ":")))
}

func private(user *models.User, chat models.Chat) bool {
	return user != nil && !user.IsBot && user.ID > 0 && chat.Type == models.ChatTypePrivate && chat.ID == user.ID
}

func (h *Handler) command(text string) (string, bool) {
	parts := strings.Fields(text)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return "", false
	}
	command, username, addressed := strings.Cut(strings.TrimPrefix(parts[0], "/"), "@")
	return strings.ToLower(command), addressed && !strings.EqualFold(username, h.username)
}

func (h *Handler) access(ctx context.Context, r request) (promotionadminentity.Access, error) {
	access, err := h.admin.Access(ctx, r.actorID)
	if err == nil && !access.Organizer && len(access.PartnerIDs) == 0 {
		err = promotionadminentity.ErrForbidden
	}
	return access, err
}

func (h *Handler) authorizeAudience(ctx context.Context, r request, audience entity.Audience) error {
	if audience.Kind == "partner" {
		return h.admin.AuthorizePartner(ctx, r.actorID, audience.PartnerID)
	}
	access, err := h.access(ctx, r)
	if err != nil {
		return err
	}
	if !access.Organizer {
		return promotionadminentity.ErrForbidden
	}
	if audience.Kind != "all" && audience.Kind != "users" {
		return promotionadminentity.ErrInvalid
	}
	return nil
}

func (h *Handler) action(ctx context.Context, r request, parts []string) error {
	if len(parts) == 4 && parts[1] == "send" {
		return h.send(ctx, r, parts[2], parts[3])
	}
	if len(parts) == 2 && parts[1] == "new" {
		return h.audiences(ctx, r)
	}
	if len(parts) == 3 && parts[1] == "audience" {
		if parts[2] == "partner" {
			return h.partners(ctx, r, 0)
		}
		f := flow{Audience: entity.Audience{Kind: parts[2]}}
		if err := h.authorizeAudience(ctx, r, f.Audience); err != nil {
			return err
		}
		if parts[2] == "users" {
			return h.begin(ctx, r, "users", f, "Пришлите числовые Telegram ID получателей через пробел, запятую или с новой строки. Бот может писать только пользователям, которые уже его запустили.")
		}
		return h.beginText(ctx, r, f)
	}
	if len(parts) != 3 {
		return promotionadminentity.ErrInvalid
	}
	if parts[1] == "list" || parts[1] == "partners" {
		page, err := strconv.Atoi(parts[2])
		if err != nil || page < 0 {
			return promotionadminentity.ErrInvalid
		}
		if parts[1] == "partners" {
			return h.partners(ctx, r, page)
		}
		return h.list(ctx, r, page)
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return promotionadminentity.ErrInvalid
	}
	if parts[1] == "partner" {
		f := flow{Audience: entity.Audience{Kind: "partner", PartnerID: id}}
		if err := h.authorizeAudience(ctx, r, f.Audience); err != nil {
			return err
		}
		return h.beginText(ctx, r, f)
	}
	var value *entity.Broadcast
	switch parts[1] {
	case "view":
		value, err = h.service.View(ctx, r.actorID, id)
	case "cancel":
		value, err = h.service.Cancel(ctx, r.actorID, id)
	default:
		return promotionadminentity.ErrInvalid
	}
	if err != nil {
		return err
	}
	return h.show(ctx, r, value)
}

func (h *Handler) input(ctx context.Context, r request, state string, f flow, text string) error {
	if err := h.authorizeAudience(ctx, r, f.Audience); err != nil {
		return err
	}
	if state == "broadcast:users" {
		ids, err := parseIDs(text)
		if err != nil {
			return err
		}
		f.Audience.TelegramIDs = ids
		return h.beginText(ctx, r, f)
	}
	if state == "broadcast:confirm" {
		return h.reply(ctx, r, "Подтвердите отправку кнопкой под предпросмотром или отмените ее командой /cancel.", back())
	}
	if state != "broadcast:text" {
		return promotionadminentity.ErrInvalid
	}
	value, err := h.service.Create(ctx, r.actorID, r.updateID, f.Audience, text)
	if err != nil {
		return err
	}
	if err := h.dialogs.Clear(ctx, r.userID); err != nil {
		return err
	}
	return h.show(ctx, r, value)
}

func parseIDs(text string) ([]int64, error) {
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	if len(parts) == 0 {
		return nil, fmt.Errorf("%w: укажите Telegram ID получателей", promotionadminentity.ErrInvalid)
	}
	ids := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%w: Telegram ID должен быть положительным числом", promotionadminentity.ErrInvalid)
		}
		if _, ok := seen[id]; !ok {
			ids = append(ids, id)
			seen[id] = struct{}{}
		}
	}
	return ids, nil
}

func (h *Handler) beginText(ctx context.Context, r request, f flow) error {
	return h.begin(ctx, r, "text", f, "Пришлите текст рассылки одним сообщением: до 4096 символов Telegram, без вложений. Далее появится предпросмотр с числом получателей и отдельной кнопкой отправки.")
}

func (h *Handler) begin(ctx context.Context, r request, state string, f flow, prompt string) error {
	payload, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := h.dialogs.Save(ctx, r.userID, "broadcast:"+state, payload, 30*time.Minute); err != nil {
		return err
	}
	return h.reply(ctx, r, prompt+"\n\n/cancel — отмена.", back())
}

func (h *Handler) respondError(ctx context.Context, r request, err error) error {
	if err == nil {
		return nil
	}
	for _, expected := range []error{promotionadminentity.ErrForbidden, promotionadminentity.ErrInvalid, promotionadminentity.ErrNotFound, promotionadminentity.ErrConflict} {
		if errors.Is(err, expected) {
			return h.reply(ctx, r, err.Error()+". Исправьте ввод или откройте список рассылок. /cancel — отмена.", back())
		}
	}
	return err
}

func (h *Handler) enqueue(ctx context.Context, r request, kind, suffix string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return h.outbox.Enqueue(ctx, r.userID, r.updateID, kind, encoded, fmt.Sprintf("telegram:update:%d:broadcast:%s", r.updateID, suffix))
}

// Отправка разрешена лишь кнопкой текущего предпросмотра. Переход в другой
// сценарий или /cancel делает старую кнопку недействительной.
func (h *Handler) send(ctx context.Context, r request, idText, nonceText string) error {
	dialog, err := h.dialogs.Load(ctx, r.userID)
	if err != nil {
		return err
	}
	if dialog == nil || dialog.State != "broadcast:confirm" {
		return h.reply(ctx, r, "Подтверждение устарело. Откройте черновик из списка и проверьте его заново.", back())
	}
	var f flow
	if err := json.Unmarshal(dialog.Payload, &f); err != nil {
		return err
	}
	if f.BroadcastID <= 0 || strconv.FormatInt(f.BroadcastID, 10) != idText || strconv.FormatInt(f.Nonce, 10) != nonceText {
		return h.reply(ctx, r, "Это подтверждение относится к другому предпросмотру. Откройте нужный черновик из списка.", back())
	}
	value, err := h.service.Send(ctx, r.actorID, f.BroadcastID)
	if err != nil {
		return err
	}
	if err := h.dialogs.Clear(ctx, r.userID); err != nil {
		return err
	}
	return h.show(ctx, r, value)
}
