package claims

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

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
	claimservice "github.com/letsssgooo/giveaways_bot/internal/services/draw/claims"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type Claims interface {
	View(context.Context, int64, int64) (*awardsentity.Award, error)
	List(context.Context, int64) ([]awardsentity.Award, error)
	AcceptConsent(context.Context, int64, int64) (*awardsentity.Award, error)
	Confirm(context.Context, int64, int64, string, string) (*awardsentity.Award, error)
}

type ClaimDialogs interface {
	Load(context.Context, int64) (*dialogs.Dialog, error)
	Save(context.Context, int64, string, json.RawMessage, time.Duration) error
	Clear(context.Context, int64) error
}

type ClaimOutbox interface {
	Enqueue(context.Context, int64, int64, string, json.RawMessage, string) error
}

type ClaimsHandler struct {
	claims     Claims
	users      *userservice.Service
	dialogs    ClaimDialogs
	outbox     ClaimOutbox
	username   string
	replyIndex int
}

func NewClaims(claims Claims, users *userservice.Service, dialogs ClaimDialogs, outbox ClaimOutbox, username string) *ClaimsHandler {
	return &ClaimsHandler{claims: claims, users: users, dialogs: dialogs, outbox: outbox, username: strings.TrimPrefix(username, "@")}
}

type claimRequest struct{ userID, telegramID, updateID int64 }
type claimFlow struct {
	AwardID   int64  `json:"award_id"`
	Nonce     int64  `json:"nonce"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

// Handle выполняется в общей транзакции входящего события: согласие, имена,
// диалог и ответы сохраняются вместе. Внешних запросов здесь нет.
func (h *ClaimsHandler) Handle(ctx context.Context, update *models.Update) (bool, error) {
	h.replyIndex = 0
	if update == nil || update.ID < 0 {
		return false, nil
	}
	if callback := update.CallbackQuery; callback != nil {
		if !strings.HasPrefix(callback.Data, "prize:") {
			return false, nil
		}
		if callback.ID == "" || callback.Message.Type != models.MaybeInaccessibleMessageTypeMessage || callback.Message.Message == nil || !claimPrivate(&callback.From, callback.Message.Message.Chat) {
			return true, nil
		}
		user, err := h.users.Ensure(ctx, callback.From.ID, callback.Message.Message.Chat.ID)
		if err != nil {
			return true, err
		}
		r := claimRequest{user.ID, callback.From.ID, int64(update.ID)}
		if err := h.enqueue(ctx, r, protocol.MessageKindCallbackAnswer, "callback", map[string]string{"callback_query_id": callback.ID}); err != nil {
			return true, err
		}
		return true, h.respondError(ctx, r, h.callback(ctx, r, callback.Data))
	}
	message := update.Message
	if message == nil || !claimPrivate(message.From, message.Chat) {
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
	r := claimRequest{user.ID, message.From.ID, int64(update.ID)}
	dialog, err := h.dialogs.Load(ctx, user.ID)
	if err != nil {
		return false, err
	}
	ownDialog := dialog != nil && strings.HasPrefix(dialog.State, "prize:")
	if command == protocol.CommandPrizes {
		if ownDialog {
			if err := h.dialogs.Clear(ctx, user.ID); err != nil {
				return true, err
			}
		}
		return true, h.respondError(ctx, r, h.list(ctx, r))
	}
	if !ownDialog {
		return false, nil
	}
	if command != "" {
		if err := h.dialogs.Clear(ctx, user.ID); err != nil {
			return true, err
		}
		if command == protocol.CommandCancel {
			return true, h.reply(ctx, r, "Ввод отменен. Вернуться к получению приза можно командой /prizes.", nil)
		}
		return false, nil
	}
	return true, h.respondError(ctx, r, h.input(ctx, r, dialog, message.Text))
}

func claimPrivate(user *models.User, chat models.Chat) bool {
	return user != nil && !user.IsBot && user.ID > 0 && chat.Type == models.ChatTypePrivate && chat.ID == user.ID
}

func (h *ClaimsHandler) command(text string) (string, bool) {
	parts := strings.Fields(text)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return "", false
	}
	command, username, addressed := strings.Cut(strings.TrimPrefix(parts[0], "/"), "@")
	return strings.ToLower(command), addressed && !strings.EqualFold(username, h.username)
}

func claimAvailable(award *awardsentity.Award) error {
	if award == nil || !award.Current {
		return awardsentity.ErrAwardUnavailable
	}
	if award.ConfirmedAt != nil {
		return nil
	}
	if award.State != "pending" && award.State != "notified" && award.State != "manual" {
		return awardsentity.ErrAwardUnavailable
	}
	if award.Deadline != nil && !award.Now.Before(*award.Deadline) {
		return awardsentity.ErrClaimExpired
	}
	if strings.TrimSpace(award.ConsentVersion) == "" || strings.TrimSpace(award.ConsentText) == "" {
		return awardsentity.ErrAwardUnavailable
	}
	return nil
}

func (h *ClaimsHandler) view(ctx context.Context, r claimRequest, awardID int64) (*awardsentity.Award, error) {
	award, err := h.claims.View(ctx, awardID, r.userID)
	if err != nil {
		return nil, err
	}
	if err := claimAvailable(award); err != nil {
		return nil, err
	}
	return award, nil
}

func (h *ClaimsHandler) callback(ctx context.Context, r claimRequest, data string) error {
	parts := strings.Split(data, ":")
	if len(parts) < 3 {
		return h.stale(ctx, r)
	}
	awardID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || awardID <= 0 {
		return h.stale(ctx, r)
	}
	if parts[1] == "claim" && len(parts) == 3 {
		return h.begin(ctx, r, awardID)
	}
	if len(parts) != 4 {
		return h.stale(ctx, r)
	}
	nonce, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return h.stale(ctx, r)
	}
	if parts[1] != "consent" && parts[1] != "confirm" && parts[1] != "decline" {
		return h.stale(ctx, r)
	}
	award, err := h.view(ctx, r, awardID)
	if err != nil {
		return err
	}
	if award.ConfirmedAt != nil {
		return h.confirmed(ctx, r, award)
	}
	dialog, err := h.dialogs.Load(ctx, r.userID)
	if err != nil {
		return err
	}
	if dialog == nil || !strings.HasPrefix(dialog.State, "prize:") {
		return h.stale(ctx, r)
	}
	var f claimFlow
	if err := json.Unmarshal(dialog.Payload, &f); err != nil {
		return err
	}
	if f.AwardID != awardID || f.Nonce != nonce {
		return h.stale(ctx, r)
	}
	return h.dialogCallback(ctx, r, parts[1], dialog.State, f)
}

func (h *ClaimsHandler) dialogCallback(ctx context.Context, r claimRequest, action, state string, f claimFlow) error {
	switch action {
	case "decline":
		if state != "prize:consent" {
			return h.stale(ctx, r)
		}
		if err := h.dialogs.Clear(ctx, r.userID); err != nil {
			return err
		}
		return h.reply(ctx, r, "Согласие не предоставлено, контактные данные не запрашиваем. Если передумаешь, открой /prizes до окончания срока ответа.", nil)
	case "consent":
		if state != "prize:consent" {
			return h.stale(ctx, r)
		}
		award, err := h.claims.AcceptConsent(ctx, f.AwardID, r.userID)
		if err != nil {
			return err
		}
		if err := claimAvailable(award); err != nil {
			return err
		}
		if award.ConfirmedAt != nil {
			return h.confirmed(ctx, r, award)
		}
		if award.ConsentGivenAt == nil {
			return awardsentity.ErrConsentRequired
		}
		return h.askFirstName(ctx, r, f)
	case "confirm":
		if state != "prize:confirm" {
			return h.stale(ctx, r)
		}
		award, err := h.claims.Confirm(ctx, f.AwardID, r.userID, f.FirstName, f.LastName)
		if err != nil {
			return err
		}
		return h.confirmed(ctx, r, award)
	default:
		return h.stale(ctx, r)
	}
}

func (h *ClaimsHandler) begin(ctx context.Context, r claimRequest, awardID int64) error {
	award, err := h.view(ctx, r, awardID)
	if err != nil {
		return err
	}
	if award.ConfirmedAt != nil {
		return h.confirmed(ctx, r, award)
	}
	f := claimFlow{AwardID: award.ID, Nonce: r.updateID}
	intro := awardDescription(award)
	if award.Instructions != "" {
		intro += "\n\n" + award.Instructions
	}
	if award.Deadline != nil {
		intro += "\n\nПодтверди получение до " + award.Deadline.In(time.FixedZone("MSK", 3*60*60)).Format("02.01.2006 15:04:05") + " МСК."
	}
	if award.ConsentGivenAt != nil {
		if err := h.reply(ctx, r, intro, nil); err != nil {
			return err
		}
		return h.askFirstName(ctx, r, f)
	}
	if err := h.save(ctx, r, "consent", f); err != nil {
		return err
	}
	text := intro + "\n\nДля передачи приза требуется согласие на передачу данных. Прочитай полный текст ниже.\n\n" + award.ConsentText
	return h.reply(ctx, r, text, claimKeyboard(
		claimButton("Согласен / согласна", fmt.Sprintf("consent:%d:%d", award.ID, f.Nonce)),
		claimButton("Не согласен / не согласна", fmt.Sprintf("decline:%d:%d", award.ID, f.Nonce)),
	))
}

func (h *ClaimsHandler) askFirstName(ctx context.Context, r claimRequest, f claimFlow) error {
	f.FirstName, f.LastName = "", ""
	if err := h.save(ctx, r, "first_name", f); err != nil {
		return err
	}
	return h.reply(ctx, r, "Напиши свое имя для передачи приза. /cancel — отменить ввод.", nil)
}

func (h *ClaimsHandler) input(ctx context.Context, r claimRequest, dialog *dialogs.Dialog, text string) error {
	var f claimFlow
	if err := json.Unmarshal(dialog.Payload, &f); err != nil {
		return err
	}
	award, err := h.view(ctx, r, f.AwardID)
	if err != nil {
		return err
	}
	if award.ConfirmedAt != nil {
		return h.confirmed(ctx, r, award)
	}
	if dialog.State == "prize:consent" {
		return h.reply(ctx, r, "Сначала прочитай текст согласия и выбери кнопку под ним. /cancel — отменить ввод.", nil)
	}
	if award.ConsentGivenAt == nil {
		return awardsentity.ErrConsentRequired
	}
	if dialog.State == "prize:confirm" {
		return h.reply(ctx, r, "Проверь имя и фамилию и нажми «Подтвердить получение». Для исправления открой /prizes.", nil)
	}
	name, err := claimservice.NormalizeName(text)
	if err != nil {
		return h.reply(ctx, r, "Пришли имя или фамилию текстом: от 1 до 100 символов, без переносов строк и управляющих символов.", nil)
	}
	switch dialog.State {
	case "prize:first_name":
		f.FirstName = name
		if err := h.save(ctx, r, "last_name", f); err != nil {
			return err
		}
		return h.reply(ctx, r, "Теперь напиши свою фамилию. /cancel — отменить ввод.", nil)
	case "prize:last_name":
		f.LastName, f.Nonce = name, r.updateID
		if err := h.save(ctx, r, "confirm", f); err != nil {
			return err
		}
		return h.reply(ctx, r, awardDescription(award)+"\n\nПроверь данные:\nИмя: "+f.FirstName+"\nФамилия: "+f.LastName+"\n\nДля исправления открой /prizes.", claimKeyboard(claimButton("Подтвердить получение", fmt.Sprintf("confirm:%d:%d", f.AwardID, f.Nonce))))
	default:
		return h.stale(ctx, r)
	}
}

func awardDescription(award *awardsentity.Award) string {
	text := "Твой приз: " + award.PrizeName
	if award.PartnerName != "" {
		text += " от " + award.PartnerName
	}
	if award.Units > 1 {
		text += fmt.Sprintf(" (%d шт.)", award.Units)
	}
	return text
}

func (h *ClaimsHandler) confirmed(ctx context.Context, r claimRequest, award *awardsentity.Award) error {
	if err := h.dialogs.Clear(ctx, r.userID); err != nil {
		return err
	}
	text := "Получение приза подтверждено ✅\n\n" + awardDescription(award)
	if award.Instructions != "" {
		text += "\n\n" + award.Instructions
	}
	return h.reply(ctx, r, text, nil)
}

func (h *ClaimsHandler) list(ctx context.Context, r claimRequest) error {
	awards, err := h.claims.List(ctx, r.userID)
	if err != nil {
		return err
	}
	if len(awards) == 0 {
		return h.reply(ctx, r, "У тебя пока нет призов с утвержденными результатами. /menu — главное меню.", nil)
	}
	for i := range awards {
		award := &awards[i]
		text := awardDescription(award)
		var markup *models.InlineKeyboardMarkup
		switch {
		case !award.Current:
			text += "\nПолучение этого приза недоступно."
		case award.ConfirmedAt != nil:
			text += "\nПолучение подтверждено ✅"
			markup = claimKeyboard(claimButton("Посмотреть инструкции", fmt.Sprintf("claim:%d", award.ID)))
		case errors.Is(claimAvailable(award), awardsentity.ErrClaimExpired):
			text += "\nСрок подтверждения получения истек."
		case claimAvailable(award) == nil:
			markup = claimKeyboard(claimButton("Получить приз", fmt.Sprintf("claim:%d", award.ID)))
		default:
			text += "\nИнструкция по получению еще готовится."
		}
		if err := h.reply(ctx, r, text, markup); err != nil {
			return err
		}
	}
	return nil
}

func (h *ClaimsHandler) respondError(ctx context.Context, r claimRequest, err error) error {
	if err == nil {
		return nil
	}
	text := ""
	switch {
	case errors.Is(err, awardsentity.ErrAwardUnavailable):
		text = "Этот приз недоступен для получения. Актуальные призы — /prizes."
	case errors.Is(err, awardsentity.ErrClaimExpired):
		text = "Срок подтверждения получения приза истек. Актуальные призы — /prizes."
	case errors.Is(err, awardsentity.ErrConsentRequired):
		text = "Для передачи приза нужно явное согласие. Открой /prizes и прочитай текст согласия."
	case errors.Is(err, claimservice.ErrInvalidName):
		text = "Имя и фамилия должны содержать от 1 до 100 символов без переносов строк и управляющих символов. Открой /prizes для исправления."
	default:
		return err
	}
	if err := h.dialogs.Clear(ctx, r.userID); err != nil {
		return err
	}
	return h.reply(ctx, r, text, nil)
}

func (h *ClaimsHandler) stale(ctx context.Context, r claimRequest) error {
	return h.reply(ctx, r, "Кнопка устарела. Продолжи текущий ввод или открой /prizes.", nil)
}

func (h *ClaimsHandler) save(ctx context.Context, r claimRequest, state string, f claimFlow) error {
	payload, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return h.dialogs.Save(ctx, r.userID, "prize:"+state, payload, 30*time.Minute)
}

func claimButton(text, action string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: "prize:" + action}
}

func claimKeyboard(buttons ...models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	markup := &models.InlineKeyboardMarkup{}
	for _, button := range buttons {
		markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{button})
	}
	return markup
}

func (h *ClaimsHandler) reply(ctx context.Context, r claimRequest, text string, markup *models.InlineKeyboardMarkup) error {
	var parts []string
	start, size := 0, 0
	for i, char := range text {
		length := utf16.RuneLen(char)
		if size+length > protocol.MaxMessageLength {
			parts = append(parts, text[start:i])
			start, size = i, 0
		}
		size += length
	}
	parts = append(parts, text[start:])
	for i, part := range parts {
		var keyboard *models.InlineKeyboardMarkup
		if i == len(parts)-1 {
			keyboard = markup
		}
		payload := struct {
			ChatID int64                        `json:"chat_id"`
			Text   string                       `json:"text"`
			Markup *models.InlineKeyboardMarkup `json:"reply_markup,omitempty"`
		}{r.telegramID, part, keyboard}
		suffix := fmt.Sprintf("reply:%d", h.replyIndex)
		h.replyIndex++
		if err := h.enqueue(ctx, r, protocol.MessageKindText, suffix, payload); err != nil {
			return err
		}
	}
	return nil
}

func (h *ClaimsHandler) enqueue(ctx context.Context, r claimRequest, kind, suffix string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return h.outbox.Enqueue(ctx, r.userID, r.updateID, kind, encoded, fmt.Sprintf("telegram:update:%d:prize:%s", r.updateID, suffix))
}
