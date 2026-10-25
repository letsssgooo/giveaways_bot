package usernames

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/api"
)

const apiMethodGetChat = "getChat"

// Check проверяет актуальный username известного пользователя по его ID.
// username передается без @. Bot API не позволяет искать личный чат по username.
func Check(ctx context.Context, sdk *bot.Bot, telegramID int64, username string) (bool, error) {
	if telegramID <= 0 || username == "" {
		return false, &api.Error{Method: apiMethodGetChat, Code: http.StatusBadRequest, Reason: "invalid user ID or username"}
	}
	chat, err := sdk.GetChat(ctx, &bot.GetChatParams{ChatID: telegramID})
	if err != nil {
		err = api.Wrap(apiMethodGetChat, err)
		if api.IsPermanent(err) {
			return false, nil
		}
		return false, err
	}
	if chat == nil || chat.ID != telegramID || chat.Type != models.ChatTypePrivate {
		return false, &api.Error{Method: apiMethodGetChat, Reason: "invalid private chat response"}
	}
	return strings.EqualFold(chat.Username, username), nil
}
