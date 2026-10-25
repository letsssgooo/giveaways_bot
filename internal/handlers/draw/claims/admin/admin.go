package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
	claimadminservice "github.com/letsssgooo/giveaways_bot/internal/services/draw/claims/admin"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type AwardAdminDialogs interface {
	Load(context.Context, int64) (*dialogs.Dialog, error)
	Save(context.Context, int64, string, json.RawMessage, time.Duration) error
	Clear(context.Context, int64) error
}

type AwardAdminOutbox interface {
	Enqueue(context.Context, int64, int64, string, json.RawMessage, string) error
}

type ClaimsAdminHandler struct {
	service *claimadminservice.ClaimAdministration
	users   *userservice.Service
	dialogs AwardAdminDialogs
	outbox  AwardAdminOutbox
}

func NewClaimsAdmin(service *claimadminservice.ClaimAdministration, users *userservice.Service, dialogs AwardAdminDialogs, outbox AwardAdminOutbox) *ClaimsAdminHandler {
	return &ClaimsAdminHandler{service: service, users: users, dialogs: dialogs, outbox: outbox}
}

type awardAdminRequest struct{ userID, telegramID, updateID int64 }
type awardAdminConfirmation struct {
	AwardID int64  `json:"award_id"`
	Action  string `json:"action"`
	Nonce   int64  `json:"nonce"`
}

func (h *ClaimsAdminHandler) Handle(ctx context.Context, update *models.Update) (bool, error) {
	if update == nil || update.ID < 0 || update.CallbackQuery == nil {
		return false, nil
	}
	callback := update.CallbackQuery
	data, found := strings.CutPrefix(callback.Data, "prizeadmin:")
	if !found {
		return false, nil
	}
	if callback.ID == "" || callback.Message.Type != models.MaybeInaccessibleMessageTypeMessage || callback.Message.Message == nil {
		return true, nil
	}
	chat := callback.Message.Message.Chat
	if callback.From.IsBot || callback.From.ID <= 0 || chat.Type != models.ChatTypePrivate || chat.ID != callback.From.ID {
		return true, nil
	}
	user, err := h.users.Ensure(ctx, callback.From.ID, chat.ID)
	if err != nil {
		return true, err
	}
	r := awardAdminRequest{user.ID, chat.ID, int64(update.ID)}
	if err := h.enqueue(ctx, r, protocol.MessageKindCallbackAnswer, "callback", map[string]string{"callback_query_id": callback.ID}); err != nil {
		return true, err
	}
	err = h.action(ctx, r, data)
	if errors.Is(err, promotionadminentity.ErrForbidden) || errors.Is(err, awardsentity.ErrAwardUnavailable) || errors.Is(err, awardsentity.ErrClaimExpired) {
		return true, h.reply(ctx, r, "Действие недоступно: проверьте права и текущее состояние приза.", nil)
	}
	return true, err
}

func (h *ClaimsAdminHandler) action(ctx context.Context, r awardAdminRequest, data string) error {
	if data == "list" || strings.HasPrefix(data, "list:") {
		return h.list(ctx, r, data)
	}
	parts := strings.Split(data, ":")
	if len(parts) != 2 {
		return h.reply(ctx, r, "Кнопка устарела. Откройте список призов заново.", nil)
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return h.reply(ctx, r, "Кнопка устарела. Откройте список призов заново.", nil)
	}
	if parts[0] == "confirm" {
		return h.confirm(ctx, r, id)
	}
	award, err := h.service.View(ctx, r.telegramID, id)
	if err != nil {
		return err
	}
	if parts[0] == "view" {
		if err := h.dialogs.Clear(ctx, r.userID); err != nil {
			return err
		}
		text := fmt.Sprintf("Приз №%d: %s\nРозыгрыш №%d, место №%d.\n", award.ID, award.PrizeName, award.DrawID, award.PlaceNumber)
		var buttons []models.InlineKeyboardButton
		switch award.State {
		case "manual":
			text += "Уведомление не доставлено. Автоматическая передача приостановлена."
			buttons = append(buttons, awardAdminButton("Повторить уведомление", fmt.Sprintf("retry:%d", id)), awardAdminButton("Передать резервисту", fmt.Sprintf("replace:%d", id)))
		case "exhausted":
			text += "Резерв исчерпан. Приз остается нераспределенным; автоматического повторного розыгрыша нет."
		default:
			text += "Состояние уже изменилось; ручное действие не требуется."
		}
		return h.reply(ctx, r, text, buttons)
	}
	if (parts[0] != "retry" && parts[0] != "replace") || award.State != "manual" || !award.Current || award.ConfirmedAt != nil {
		return awardsentity.ErrAwardUnavailable
	}
	f := awardAdminConfirmation{AwardID: id, Action: parts[0], Nonce: r.updateID}
	payload, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := h.dialogs.Save(ctx, r.userID, "prizeadmin:confirm", payload, 30*time.Minute); err != nil {
		return err
	}
	text := fmt.Sprintf("Повторить доставку уведомления о призе №%d?", id)
	if f.Action == "replace" {
		text = fmt.Sprintf("Передать приз №%d следующему свободному резервисту? Прежний победитель потеряет возможность подтвердить получение. Если резерв пуст, приз останется нераспределенным.", id)
	}
	return h.reply(ctx, r, text, []models.InlineKeyboardButton{awardAdminButton("Подтвердить", fmt.Sprintf("confirm:%d", r.updateID))})
}

func (h *ClaimsAdminHandler) list(ctx context.Context, r awardAdminRequest, data string) error {
	page := 0
	if value, ok := strings.CutPrefix(data, "list:"); ok {
		var err error
		page, err = strconv.Atoi(value)
		if err != nil || page < 0 {
			return awardsentity.ErrAwardUnavailable
		}
	}
	if err := h.dialogs.Clear(ctx, r.userID); err != nil {
		return err
	}
	awards, err := h.service.Issues(ctx, r.telegramID)
	if err != nil {
		return err
	}
	text := "Призы, требующие ручного разбора. При недоставке можно повторить уведомление или передать приз следующему резервисту."
	const pageSize = 10
	page = min(page, max(0, (len(awards)-1)/pageSize))
	start, end := page*pageSize, min((page+1)*pageSize, len(awards))
	buttons := make([]models.InlineKeyboardButton, 0, pageSize+2)
	for _, award := range awards[start:end] {
		buttons = append(buttons, awardAdminButton(fmt.Sprintf("Приз №%d · %s", award.ID, award.PrizeName), fmt.Sprintf("view:%d", award.ID)))
	}
	if len(awards) == 0 {
		text = "Призов, требующих ручного разбора, нет."
	}
	if page > 0 {
		buttons = append(buttons, awardAdminButton("Назад", fmt.Sprintf("list:%d", page-1)))
	}
	if end < len(awards) {
		buttons = append(buttons, awardAdminButton("Дальше", fmt.Sprintf("list:%d", page+1)))
	}
	return h.reply(ctx, r, text, buttons)
}

func (h *ClaimsAdminHandler) confirm(ctx context.Context, r awardAdminRequest, nonce int64) error {
	dialog, err := h.dialogs.Load(ctx, r.userID)
	if err != nil {
		return err
	}
	var f awardAdminConfirmation
	if dialog == nil || dialog.State != "prizeadmin:confirm" {
		return h.reply(ctx, r, "Подтверждение устарело. Откройте список призов заново.", nil)
	}
	if err := json.Unmarshal(dialog.Payload, &f); err != nil {
		return err
	}
	if f.Nonce != nonce {
		return h.reply(ctx, r, "Это подтверждение относится к другому действию.", nil)
	}
	switch f.Action {
	case "retry":
		err = h.service.Retry(ctx, r.telegramID, f.AwardID)
	case "replace":
		err = h.service.Replace(ctx, r.telegramID, f.AwardID)
	default:
		return awardsentity.ErrAwardUnavailable
	}
	if err != nil {
		return err
	}
	if err := h.dialogs.Clear(ctx, r.userID); err != nil {
		return err
	}
	return h.reply(ctx, r, "Решение сохранено. Текущее состояние доступно в списке призов.", nil)
}

func awardAdminButton(text, action string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: "prizeadmin:" + action}
}

func (h *ClaimsAdminHandler) reply(ctx context.Context, r awardAdminRequest, text string, buttons []models.InlineKeyboardButton) error {
	markup := &models.InlineKeyboardMarkup{}
	for _, button := range buttons {
		markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{button})
	}
	markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{awardAdminButton("К списку призов", "list")}, []models.InlineKeyboardButton{{Text: "В админку", CallbackData: "admin:home"}})
	return h.enqueue(ctx, r, protocol.MessageKindText, "reply", struct {
		ChatID int64                        `json:"chat_id"`
		Text   string                       `json:"text"`
		Markup *models.InlineKeyboardMarkup `json:"reply_markup"`
	}{r.telegramID, text, markup})
}

func (h *ClaimsAdminHandler) enqueue(ctx context.Context, r awardAdminRequest, kind, suffix string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return h.outbox.Enqueue(ctx, r.userID, r.updateID, kind, encoded, fmt.Sprintf("telegram:update:%d:prizeadmin:%s", r.updateID, suffix))
}
