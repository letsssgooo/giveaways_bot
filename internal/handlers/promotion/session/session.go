// Package session содержит общий контекст диалога управления акцией.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	promoimport "github.com/letsssgooo/giveaways_bot/internal/services/promotion/importer"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type Outbox interface {
	Enqueue(context.Context, int64, int64, string, json.RawMessage, string) error
}

type Dialogs interface {
	Load(context.Context, int64) (*dialogs.Dialog, error)
	Save(context.Context, int64, string, json.RawMessage, time.Duration) error
	Clear(context.Context, int64) error
}

type Request struct{ UserID, ActorID, UpdateID int64 }

type Flow struct {
	Action      string `json:"action"`
	Target      int64  `json:"target"`
	Parent      int64  `json:"parent"`
	Text        string `json:"text"`
	Nonce       int64  `json:"nonce"`
	RecipientID int64  `json:"recipient_id,omitempty"`
}

func (h *Session) Access(ctx context.Context, r Request) (promotionadminentity.Access, error) {
	access, err := h.Admin.Access(ctx, r.ActorID)
	if err == nil && !access.Organizer && len(access.PartnerIDs) == 0 {
		err = promotionadmin.ErrForbidden
	}
	return access, err
}

func (h *Session) RespondError(ctx context.Context, r Request, err error) error {
	if err == nil {
		return nil
	}
	for _, expected := range []error{promotionadmin.ErrForbidden, promotionadmin.ErrInvalid, promotionadmin.ErrNotFound, promotionadmin.ErrConflict} {
		if errors.Is(err, expected) {
			return h.Reply(ctx, r, err.Error()+". Откройте /admin или отмените ввод: /cancel.", Back())
		}
	}
	return err
}

func Button(text, action string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: "admin:" + action}
}

func Keyboard(buttons ...models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	markup := &models.InlineKeyboardMarkup{}
	for _, b := range buttons {
		markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{b})
	}
	return markup
}

func Back() *models.InlineKeyboardMarkup { return Keyboard(Button("В админку", "home")) }

func (h *Session) Reply(ctx context.Context, r Request, text string, markup *models.InlineKeyboardMarkup) error {
	var parts []string
	start, size := 0, 0
	for i, c := range text {
		n := utf16.RuneLen(c)
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
		}{r.ActorID, part, kb}
		if err := h.Enqueue(ctx, r, protocol.MessageKindText, fmt.Sprintf("reply:%d", i), payload); err != nil {
			return err
		}
	}
	return nil
}

func (h *Session) Enqueue(ctx context.Context, r Request, kind, suffix string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return h.Outbox.Enqueue(ctx, r.UserID, r.UpdateID, kind, encoded, fmt.Sprintf("telegram:update:%d:admin:%s", r.UpdateID, suffix))
}

func (h *Session) SaveFlow(ctx context.Context, r Request, state string, f Flow) error {
	payload, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return h.Dialogs.Save(ctx, r.UserID, "admin:"+state, payload, 30*time.Minute)
}

func (h *Session) Begin(ctx context.Context, r Request, f Flow, prompt string) error {
	if err := h.SaveFlow(ctx, r, "input", f); err != nil {
		return err
	}
	return h.Reply(ctx, r, prompt+"\n\nПришлите данные одним сообщением. Изменение потребует подтверждения. /cancel — отмена.", Back())
}

func (h *Session) Confirm(ctx context.Context, r Request, f Flow) error {
	if err := h.SaveFlow(ctx, r, "confirm", f); err != nil {
		return err
	}
	preview := f.Text
	if f.Action == "toggle" {
		preview = fmt.Sprintf("Отключить прием в день #%d", f.Target)
		if f.Text == "true" {
			preview = fmt.Sprintf("Включить прием в день #%d", f.Target)
		}
	}
	if f.Action == "import" {
		preview = fmt.Sprintf("Импорт кодов партнеру #%d: %d непустых строк. Уже загруженные коды будут пропущены.", f.Target, len(NonemptyLines(f.Text)))
	}
	if f.Action == "grant" || f.Action == "revoke" {
		role := "организатор"
		if f.Target != 0 {
			role = fmt.Sprintf("администратор партнера #%d", f.Target)
		}
		verb := "Выдать"
		if f.Action == "revoke" {
			verb = "Отозвать"
		}
		preview = fmt.Sprintf("%s доступ: %s. Telegram ID: %s", verb, role, f.Text)
		if strings.HasPrefix(f.Text, "@") {
			preview = fmt.Sprintf("%s доступ: %s. Пользователь: %s (Telegram ID: %d)", verb, role, f.Text, f.RecipientID)
		}
	}
	return h.Reply(ctx, r, "Подтвердите изменение:\n\n"+preview, Keyboard(Button("Подтвердить", fmt.Sprintf("confirm:%d", f.Nonce)), Button("Отмена", "home")))
}

func ParseID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%w: нужен положительный числовой ID", promotionadmin.ErrInvalid)
	}
	return id, nil
}

// GrantVerification относится к одному update. Telegram проверяется приложением
// до транзакции; подтверждение сохраняет доступ за показанным числовым ID.
type GrantVerification struct {
	UpdateID   int64
	TelegramID int64
	Username   string
	Problem    string
}

func Lines(text string) []string {
	values := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	return values
}

func NonemptyLines(text string) []string {
	var result []string
	for _, line := range Lines(text) {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func FormError(text string) error { return fmt.Errorf("%w: %s", promotionadmin.ErrInvalid, text) }

type Session struct {
	Admin             *promotionadmin.AdminService
	Importer          *promoimport.ImportService
	Users             *userservice.Service
	Dialogs           Dialogs
	Outbox            Outbox
	Username          string
	GrantVerification GrantVerification
}

func (h *Session) FindDay(ctx context.Context, r Request, id int64) (promotionadminentity.DayConfig, error) {
	days, err := h.Admin.Days(ctx, r.ActorID)
	if err != nil {
		return promotionadminentity.DayConfig{}, err
	}
	for _, d := range days {
		if d.ID == id {
			return d, nil
		}
	}
	return promotionadminentity.DayConfig{}, promotionadmin.ErrNotFound
}
