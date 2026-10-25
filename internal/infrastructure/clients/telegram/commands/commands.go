package commands

import (
	"context"
	"net/http"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/api"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

const (
	apiMethodDeleteMyCommands = "deleteMyCommands"
	apiMethodSetMyCommands    = "setMyCommands"
)

// Sync устанавливает меню администратора в личном чате либо возвращает
// общий список команд. Проверка прав принадлежит вызывающему коду.
func Sync(ctx context.Context, sdk *bot.Bot, chatID int64, admin bool) error {
	method := apiMethodDeleteMyCommands
	if admin {
		method = apiMethodSetMyCommands
	}
	if chatID <= 0 {
		return &api.Error{Method: method, Code: http.StatusBadRequest, Reason: "invalid private chat ID"}
	}
	scope := &models.BotCommandScopeChat{ChatID: chatID}
	var ok bool
	var err error
	if admin {
		ok, err = sdk.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: List(true), Scope: scope})
	} else {
		ok, err = sdk.DeleteMyCommands(ctx, &bot.DeleteMyCommandsParams{Scope: scope})
	}
	if err != nil {
		return api.Wrap(method, err)
	}
	if !ok {
		return &api.Error{Method: method, Reason: "operation was not confirmed"}
	}
	return nil
}

func List(admin bool) []models.BotCommand {
	commands := []models.BotCommand{
		{Command: protocol.CommandStart, Description: "Начать работу с ботом"},
		{Command: protocol.CommandMenu, Description: "Главное меню"},
		{Command: protocol.CommandHelp, Description: "Справка"},
	}
	if admin {
		commands = append(commands, models.BotCommand{Command: protocol.CommandAdmin, Description: "Управление акцией для администраторов"})
	}
	commands = append(commands, models.BotCommand{Command: protocol.CommandCancel, Description: "Отменить текущий ввод"})
	return append(commands, models.BotCommand{Command: protocol.CommandPrizes, Description: "Мои призы и получение"})
}
