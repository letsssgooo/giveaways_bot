package users

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	registrationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation/registration"
	chanceshandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users/chances"
	codeshandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users/codes"
	menuhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users/menu"
	registrationhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users/registration"
	usersession "github.com/letsssgooo/giveaways_bot/internal/handlers/users/session"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
	participationService "github.com/letsssgooo/giveaways_bot/internal/services/participation"
	promotionService "github.com/letsssgooo/giveaways_bot/internal/services/promotion"
	userService "github.com/letsssgooo/giveaways_bot/internal/services/users"
	telegramprotocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type Handler struct{ *usersession.Session }

func New(users *userService.Service, promotion *promotionService.Service, participation *participationService.Service, outbox usersession.Outbox, botUsername string) *Handler {
	return &Handler{Session: &usersession.Session{Users: users, Promotion: promotion, Participation: participation, Outbox: outbox, BotUsername: strings.TrimPrefix(botUsername, "@")}}
}

const callbackKeySuffix = "callback"

// Handle вызывается внутри транзакции обработки входящего события: пользователь,
// ответы и отметка обработки сохраняются вместе.
func (h *Handler) Handle(ctx context.Context, update *models.Update) error {
	if update == nil || update.ID < 0 {
		return nil
	}
	if update.CallbackQuery != nil {
		return h.callback(ctx, update.ID, update.CallbackQuery)
	}
	if update.Message != nil {
		return h.message(ctx, update.ID, update.Message)
	}
	return nil
}

func (h *Handler) message(ctx context.Context, updateID int64, message *models.Message) error {
	if !usersession.PrivateUser(message.From, message.Chat) {
		return nil
	}
	command, addressedElsewhere := h.command(message.Text)
	if addressedElsewhere {
		return nil
	}
	user, err := h.Users.Ensure(ctx, message.From.ID, message.Chat.ID)
	if err != nil {
		return err
	}
	switch command {
	case telegramprotocol.CommandStart:
		return menuhandler.Start(ctx, h.Session, user.ID, updateID, message.Chat.ID)
	case telegramprotocol.CommandMenu:
		return h.Reply(ctx, user.ID, updateID, message.Chat.ID, userview.WelcomeText, userview.MainKeyboard())
	case telegramprotocol.CommandHelp:
		return h.Reply(ctx, user.ID, updateID, message.Chat.ID, userview.HelpText, userview.HomeKeyboard())
	default:
		return h.Reply(ctx, user.ID, updateID, message.Chat.ID, userview.InvalidInputText, userview.HomeKeyboard())
	}
}

func (h *Handler) callback(ctx context.Context, updateID int64, callback *models.CallbackQuery) error {
	if callback.ID == "" {
		return nil
	}
	var chat models.Chat
	var inaccessible bool
	switch callback.Message.Type {
	case models.MaybeInaccessibleMessageTypeMessage:
		if callback.Message.Message == nil {
			return nil
		}
		chat = callback.Message.Message.Chat
	case models.MaybeInaccessibleMessageTypeInaccessibleMessage:
		if callback.Message.InaccessibleMessage == nil {
			return nil
		}
		chat = callback.Message.InaccessibleMessage.Chat
		inaccessible = true
	default:
		return nil
	}
	if !usersession.PrivateUser(&callback.From, chat) {
		return nil
	}
	user, err := h.Users.Ensure(ctx, callback.From.ID, chat.ID)
	if err != nil {
		return err
	}
	acknowledgment := ""
	if inaccessible || !KnownAction(callback.Data) {
		acknowledgment = userview.StaleButtonText
	}
	if err := h.Enqueue(ctx, user.ID, updateID, telegramprotocol.MessageKindCallbackAnswer, callbackKeySuffix, callbackAnswer{
		CallbackQueryID: callback.ID,
		Text:            acknowledgment,
	}); err != nil {
		return err
	}
	if inaccessible {
		return nil
	}
	return h.action(ctx, user.ID, updateID, chat.ID, callback.Data)
}

func (h *Handler) command(text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", false
	}
	command, username, addressed := strings.Cut(strings.TrimPrefix(fields[0], "/"), "@")
	if addressed && !strings.EqualFold(username, h.BotUsername) {
		return "", true
	}
	return strings.ToLower(command), false
}

type callbackAnswer struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
}

// WithRegistration подключает результат внешней проверки только к обработчику
// текущего update. Сама проверка выполнена до открытия транзакции.
func (h *Handler) WithRegistration(service usersession.Registration, verification registrationentity.Verification) *Handler {
	h.Registration, h.Verification = service, verification
	return h
}

func KnownAction(data string) bool {
	switch data {
	case userview.ActionHelp, userview.ActionHome, userview.ActionCode, userview.ActionJoin, userview.ActionHow, userview.ActionChances, userview.ActionRules:
		return true
	default:
		if _, ok := registrationhandler.CheckAction(data); ok {
			return true
		}
		if _, ok := codeshandler.PageAction(data); ok {
			return true
		}
		if _, ok := chanceshandler.PageAction(data); ok {
			return true
		}
		_, _, ok := partnerAction(data)
		return ok
	}
}

func (h *Handler) action(ctx context.Context, userID, updateID, chatID int64, data string) error {
	switch data {
	case userview.ActionHome:
		return h.Reply(ctx, userID, updateID, chatID, userview.WelcomeText, userview.MainKeyboard())
	case userview.ActionHelp:
		return h.Reply(ctx, userID, updateID, chatID, userview.HelpText, userview.HomeKeyboard())
	case userview.ActionHow, userview.ActionRules:
		return menuhandler.Material(ctx, h.Session, userID, updateID, chatID, data)
	case userview.ActionChances:
		return chanceshandler.ShowChances(ctx, h.Session, userID, updateID, chatID, 0)
	case userview.ActionCode:
		return codeshandler.ChooseCode(ctx, h.Session, userID, updateID, chatID, 0, true)
	case userview.ActionJoin:
		return menuhandler.ChoosePartner(ctx, h.Session, userID, updateID, chatID)
	default:
		if dayID, ok := registrationhandler.CheckAction(data); ok {
			return registrationhandler.CheckRegistration(ctx, h.Session, userID, updateID, chatID, dayID)
		}
		if page, ok := codeshandler.PageAction(data); ok {
			return codeshandler.ChooseCode(ctx, h.Session, userID, updateID, chatID, page, false)
		}
		if page, ok := chanceshandler.PageAction(data); ok {
			return chanceshandler.ShowChances(ctx, h.Session, userID, updateID, chatID, page)
		}
		if action, dayID, ok := partnerAction(data); ok {
			if action == userview.PartnerActionCode {
				return codeshandler.RequestCode(ctx, h.Session, userID, updateID, chatID, dayID)
			}
			return registrationhandler.JoinPartner(ctx, h.Session, userID, updateID, chatID, dayID)
		}
		return h.Reply(ctx, userID, updateID, chatID, userview.StaleButtonText, userview.HomeKeyboard())
	}
}

func partnerAction(data string) (action string, dayID int64, ok bool) {
	parts := strings.Split(data, ":")
	if len(parts) != userview.PartnerActionParts || parts[0] != userview.PartnerActionGroup || (parts[1] != userview.PartnerActionCode && parts[1] != userview.PartnerActionJoin) {
		return "", 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	return parts[1], id, err == nil && id > 0
}
