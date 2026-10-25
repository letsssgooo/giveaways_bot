// Package grants проверяет получателя административного доступа.
package grants

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	session "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/session"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
)

// GrantUsernameRequest распознает ввод ника и подтверждение конкретного черновика.
// При подтверждении возвращается ID из предпросмотра, без повторного поиска по нику.
func GrantUsernameRequest(update *models.Update, dialog *dialogs.Dialog) (username string, telegramID int64, ok bool) {
	if update == nil || dialog == nil {
		return "", 0, false
	}
	var f session.Flow
	if json.Unmarshal(dialog.Payload, &f) != nil || f.Action != "grant" {
		return "", 0, false
	}
	switch {
	case dialog.State == "admin:input" && update.Message != nil:
		f.Text = strings.TrimSpace(update.Message.Text)
	case dialog.State == "admin:confirm" && update.CallbackQuery != nil &&
		update.CallbackQuery.Data == "admin:confirm:"+strconv.FormatInt(f.Nonce, 10):
		telegramID = f.RecipientID
		if telegramID <= 0 {
			return "", 0, false
		}
	default:
		return "", 0, false
	}
	username, err := Username(f.Text)
	return username, telegramID, err == nil
}

func Username(text string) (string, error) {
	if !strings.HasPrefix(text, "@") || len(text) < 2 || len(text) > 33 {
		return "", session.FormError("укажите @username или положительный числовой Telegram ID")
	}
	username := strings.TrimPrefix(text, "@")
	for _, char := range username {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_'
		if !valid {
			return "", session.FormError("в @username допустимы латинские буквы, цифры и знак подчеркивания")
		}
	}
	return strings.ToLower(username), nil
}

func Recipient(r session.Request, f session.Flow, verified session.GrantVerification) (int64, error) {
	if !strings.HasPrefix(f.Text, "@") {
		return session.ParseID(f.Text)
	}
	username, err := Username(f.Text)
	if err != nil {
		return 0, err
	}
	if verified.UpdateID != r.UpdateID || verified.Username != username {
		return 0, session.FormError("не удалось проверить ник; повторите добавление пользователя")
	}
	if verified.Problem != "" {
		return 0, session.FormError(verified.Problem)
	}
	if verified.TelegramID <= 0 || f.RecipientID != 0 && f.RecipientID != verified.TelegramID {
		return 0, session.FormError("пользователь изменился; повторите добавление")
	}
	return verified.TelegramID, nil
}
