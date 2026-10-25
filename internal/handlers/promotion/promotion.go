// Package promotion обрабатывает Telegram-команды управления акцией.
package promotion

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/go-telegram/bot/models"

	forms "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/forms"
	grants "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/grants"
	navigation "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/navigation"
	session "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/session"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	promoimport "github.com/letsssgooo/giveaways_bot/internal/services/promotion/importer"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type Handler struct{ *session.Session }

type Outbox = session.Outbox
type Dialogs = session.Dialogs

func New(admin *promotionadmin.AdminService, importer *promoimport.ImportService, users *userservice.Service, dialogs Dialogs, outbox Outbox, username string) *Handler {
	return &Handler{Session: &session.Session{Admin: admin, Importer: importer, Users: users, Dialogs: dialogs, Outbox: outbox, Username: strings.TrimPrefix(username, "@")}}
}

// Handle возвращает false для обычного пользовательского сценария.
// Все изменения, состояние диалога и ответы фиксируются транзакцией update.
func (h *Handler) Handle(ctx context.Context, update *models.Update) (bool, error) {
	if update == nil || update.ID < 0 {
		return false, nil
	}
	if callback := update.CallbackQuery; callback != nil {
		return h.handleCallback(ctx, update.ID, callback)
	}
	message := update.Message
	if message == nil || !private(message.From, message.Chat) {
		return false, nil
	}
	command, elsewhere := h.command(message.Text)
	if elsewhere {
		return false, nil
	}
	user, err := h.Users.Ensure(ctx, message.From.ID, message.Chat.ID)
	if err != nil {
		return false, err
	}
	r := session.Request{
		UserID: user.ID, ActorID: message.From.ID, UpdateID: int64(update.ID),
	}
	switch command {
	case protocol.CommandAdmin:
		if err := h.Dialogs.Clear(ctx, user.ID); err != nil {
			return true, err
		}
		return true, h.RespondError(ctx, r, (&navigation.Handler{Session: h.Session}).Home(ctx, r))
	case protocol.CommandCancel:
		if err := h.Dialogs.Clear(ctx, user.ID); err != nil {
			return true, err
		}
		return true, h.Reply(ctx, r, "Ввод отменен. /admin — управление акцией, /menu — меню участника.", nil)
	}
	dialog, err := h.Dialogs.Load(ctx, user.ID)
	if err != nil {
		return false, err
	}
	if dialog == nil || !strings.HasPrefix(dialog.State, "admin:") {
		return false, nil
	}
	if command != "" {
		return false, h.Dialogs.Clear(ctx, user.ID)
	}
	return true, h.handleInput(ctx, r, message, dialog)
}

func (h *Handler) handleCallback(ctx context.Context, updateID int64, callback *models.CallbackQuery) (bool, error) {
	if !strings.HasPrefix(callback.Data, "admin:") || callback.ID == "" {
		return false, nil
	}
	var chat models.Chat
	inaccessible := callback.Message.Type == models.MaybeInaccessibleMessageTypeInaccessibleMessage
	if inaccessible && callback.Message.InaccessibleMessage != nil {
		chat = callback.Message.InaccessibleMessage.Chat
	} else if callback.Message.Type == models.MaybeInaccessibleMessageTypeMessage && callback.Message.Message != nil {
		chat = callback.Message.Message.Chat
	}
	if !private(&callback.From, chat) {
		return true, nil
	}
	user, err := h.Users.Ensure(ctx, callback.From.ID, chat.ID)
	if err != nil {
		return true, err
	}
	r := session.Request{
		UserID: user.ID, ActorID: callback.From.ID, UpdateID: updateID,
	}
	if err := h.Enqueue(ctx, r, protocol.MessageKindCallbackAnswer, "callback", map[string]string{"callback_query_id": callback.ID}); err != nil {
		return true, err
	}
	if inaccessible {
		return true, h.Reply(ctx, r, "Кнопка устарела. Откройте /admin.", session.Back())
	}
	return true, h.RespondError(ctx, r, (&navigation.Handler{Session: h.Session}).Callback(ctx, r, strings.TrimPrefix(callback.Data, "admin:")))
}

func (h *Handler) handleInput(ctx context.Context, r session.Request, message *models.Message, dialog *dialogs.Dialog) error {
	if _, err := h.Access(ctx, r); err != nil {
		return h.RespondError(ctx, r, err)
	}
	if dialog.State != "admin:input" {
		return h.Reply(ctx, r, "Подтвердите изменение кнопкой ниже или отмените его командой /cancel.", session.Back())
	}
	var f session.Flow
	if err := json.Unmarshal(dialog.Payload, &f); err != nil {
		return err
	}
	if message.Photo != nil && f.Action == "banner" && len(message.Photo) > 0 {
		f.Text = message.Photo[len(message.Photo)-1].FileID
	} else {
		f.Text = strings.TrimSpace(message.Text)
	}
	if f.Text == "" {
		return h.Reply(ctx, r, "Пришлите текст в указанном формате. Для баннера можно отправить фотографию. /cancel — отмена.", nil)
	}
	if err := forms.Validate(f); err != nil {
		return h.Reply(ctx, r, err.Error()+"\n\nИсправьте сообщение или отправьте /cancel.", nil)
	}
	if f.Action == "grant" {
		var err error
		f.RecipientID, err = grants.Recipient(r, f, h.GrantVerification)
		if err != nil {
			return h.RespondError(ctx, r, err)
		}
	}
	f.Nonce = r.UpdateID
	return h.Confirm(ctx, r, f)
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
	return strings.ToLower(command), addressed && !strings.EqualFold(username, h.Username)
}

// GrantVerification относится к одному update; проверка Telegram выполняется до транзакции.
type GrantVerification = session.GrantVerification

func (h *Handler) WithGrantVerification(value GrantVerification) *Handler {
	if h.Session == nil {
		h.Session = &session.Session{}
	}
	h.GrantVerification = value
	return h
}

// GrantUsernameRequest распознает ввод ника и подтверждение конкретного черновика.
func GrantUsernameRequest(update *models.Update, dialog *dialogs.Dialog) (string, int64, bool) {
	return grants.GrantUsernameRequest(update, dialog)
}
