package registration

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/participation"
	registrationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation/registration"
	usersession "github.com/letsssgooo/giveaways_bot/internal/handlers/users/session"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
)

const registrationCheckPrefix = "registration:check:"

const (
	SubscriptionPrompt  = "Чтобы попасть в розыгрыш призов от %s, подпишись на канал или группу партнера."
	SubscriptionMissing = "Пока не вижу подписку 👀\n\nПодпишись на %s, вернись в бот и попробуй еще раз."
	SubscriptionFailure = "Не удалось проверить подписку. Попробуйте еще раз чуть позже."
	RegistrationSuccess = "Ты в лайнапе ✅\n\nЗаявка на розыгрыш призов от %s зарегистрирована.\nНакоплено шансов: %d из %d."
	AlreadyRegistered   = "Ты уже участвуешь в этом розыгрыше ✅\n\nРезультаты сообщим после подведения итогов."
)

func CheckAction(data string) (int64, bool) {
	value, found := strings.CutPrefix(data, registrationCheckPrefix)
	if !found {
		return 0, false
	}
	dayID, err := strconv.ParseInt(value, 10, 64)
	return dayID, err == nil && dayID > 0
}

// RegistrationCheck проверяет транспортные данные до вызова внешнего API.
// Для групповых, чужих и недоступных сообщений запросы подписки не выполняются.
func RegistrationCheck(update *models.Update) (telegramID, dayID int64, ok bool) {
	if update == nil || update.ID < 0 || update.CallbackQuery == nil {
		return 0, 0, false
	}
	callback := update.CallbackQuery
	if callback.ID == "" || callback.Message.Type != models.MaybeInaccessibleMessageTypeMessage ||
		callback.Message.Message == nil || !usersession.PrivateUser(&callback.From, callback.Message.Message.Chat) {
		return 0, 0, false
	}
	dayID, ok = CheckAction(callback.Data)
	return callback.From.ID, dayID, ok
}

func JoinPartner(ctx context.Context, h *usersession.Session, userID, updateID, chatID, dayID int64) error {
	if h.Registration == nil {
		return h.Reply(ctx, userID, updateID, chatID, SubscriptionFailure, userview.HomeKeyboard())
	}
	day, err := h.Registration.View(ctx, chatID, dayID)
	if err != nil {
		return err
	}
	if day == nil {
		return h.Reply(ctx, userID, updateID, chatID, userview.ClosedPartnerText, userview.HomeKeyboard())
	}
	if day.Registered {
		return h.Reply(ctx, userID, updateID, chatID, AlreadyRegistered, userview.HomeKeyboard())
	}
	if !day.Open {
		return h.Reply(ctx, userID, updateID, chatID, userview.ClosedPartnerText, userview.HomeKeyboard())
	}
	return h.Reply(ctx, userID, updateID, chatID, fmt.Sprintf(SubscriptionPrompt, day.Name), subscriptionKeyboard(day, false))
}

func CheckRegistration(ctx context.Context, h *usersession.Session, userID, updateID, chatID, dayID int64) error {
	if h.Registration == nil {
		return h.Reply(ctx, userID, updateID, chatID, SubscriptionFailure, userview.HomeKeyboard())
	}
	result, err := h.Registration.Register(ctx, userID, chatID, dayID, h.Verification)
	if err != nil {
		return err
	}
	text := userview.ClosedPartnerText
	keyboard := userview.HomeKeyboard()
	switch result.Status {
	case registrationentity.Registered:
		text = fmt.Sprintf(RegistrationSuccess, result.Day.Name, result.Chances, entity.MaxFinalChances)
	case registrationentity.AlreadyRegistered:
		text = AlreadyRegistered
	case registrationentity.NotSubscribed:
		text = fmt.Sprintf(SubscriptionMissing, result.Day.Name)
		keyboard = subscriptionKeyboard(result.Day, true)
	case registrationentity.VerificationFailed:
		text = SubscriptionFailure
		keyboard = subscriptionKeyboard(result.Day, true)
	case registrationentity.Closed, registrationentity.Unavailable:
	default:
		return fmt.Errorf("unknown registration status: %q", result.Status)
	}
	return h.Reply(ctx, userID, updateID, chatID, text, keyboard)
}

func subscriptionKeyboard(day *registrationentity.RegistrationDay, retry bool) *models.InlineKeyboardMarkup {
	keyboard := &models.InlineKeyboardMarkup{}
	// Некорректная ссылка в материалах не должна ломать доставку всего ответа.
	parsed, err := url.Parse(day.SubscriptionURL)
	if err == nil && parsed.Scheme == "https" && (parsed.Host == "t.me" || parsed.Host == "telegram.me") &&
		parsed.User == nil && strings.Trim(parsed.Path, "/") != "" {
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, []models.InlineKeyboardButton{{
			Text: "Подписаться на " + day.Name, URL: day.SubscriptionURL,
		}})
	}
	label := "Проверить подписку"
	if retry {
		label = "Проверить еще раз"
	}
	keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, []models.InlineKeyboardButton{{
		Text: label, CallbackData: registrationCheckPrefix + strconv.FormatInt(day.ID, 10),
	}})
	keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, userview.HomeKeyboard().InlineKeyboard...)
	return keyboard
}
