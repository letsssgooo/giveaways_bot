package testutil

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-telegram/bot/models"
	participationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation"
	registrationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation/registration"
	promotionentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion"
	chancesentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/chances"
	userentity "github.com/letsssgooo/giveaways_bot/internal/entity/users"
	usershandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users"
	participationservice "github.com/letsssgooo/giveaways_bot/internal/services/participation"
	promotionservice "github.com/letsssgooo/giveaways_bot/internal/services/promotion"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
)

type UserRepository struct {
	Calls int
	Err   error
}

func (r *UserRepository) Ensure(_ context.Context, telegramID, chatID int64) (userentity.User, error) {
	r.Calls++
	return userentity.User{ID: 3, TelegramID: telegramID, ChatID: chatID}, r.Err
}

type QueuedMessage struct {
	UserID   int64
	UpdateID int64
	Kind     string
	Payload  json.RawMessage
	Key      string
}

type Outbox struct {
	Messages []QueuedMessage
	Err      error
}

func (o *Outbox) Enqueue(_ context.Context, userID, updateID int64, kind string, payload json.RawMessage, key string) error {
	if o.Err != nil {
		return o.Err
	}
	o.Messages = append(o.Messages, QueuedMessage{userID, updateID, kind, payload, key})
	return nil
}

func NewHandler() (*usershandler.Handler, *UserRepository, *Outbox) {
	repository, queue := &UserRepository{}, &Outbox{}
	return usershandler.New(userservice.New(repository), promotionservice.New(&PromotionRepository{}), participationservice.New(&CodeRepository{}), queue, "GiveawaysBot"), repository, queue
}

func PrivateMessage(text string) *models.Update {
	return &models.Update{ID: 42, Message: &models.Message{
		ID: 10, From: &models.User{ID: 101}, Chat: models.Chat{ID: 101, Type: models.ChatTypePrivate}, Text: text,
	}}
}

func PrivateCallback(data string) *models.Update {
	return &models.Update{ID: 43, CallbackQuery: &models.CallbackQuery{
		ID: "query-id", From: models.User{ID: 101}, Data: data,
		Message: models.MaybeInaccessibleMessage{Message: PrivateMessage("").Message},
	}}
}

type PromotionRepository struct {
	Promotion   *promotionentity.Promotion
	Partners    []promotionentity.PartnerDay
	ChancesData chancesentity.Chances
	Err         error
}

func (r *PromotionRepository) Current(context.Context) (*promotionentity.Promotion, error) {
	return r.Promotion, r.Err
}

func (r *PromotionRepository) AvailablePartners(context.Context) ([]promotionentity.PartnerDay, error) {
	return r.Partners, r.Err
}

func (r *PromotionRepository) Chances(context.Context, int64) (chancesentity.Chances, error) {
	return r.ChancesData, r.Err
}

func LastReply(t *testing.T, queue *Outbox) MessageReply {
	t.Helper()
	if len(queue.Messages) == 0 {
		t.Fatal("no reply")
	}
	var result MessageReply
	if err := json.Unmarshal(queue.Messages[len(queue.Messages)-1].Payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

type CodeRepository struct {
	Options                 []participationentity.CodeOption
	Result                  participationentity.CodeResult
	Err                     error
	Requests                int
	UserID, DayID, UpdateID int64
}

func (r *CodeRepository) CodeOptions(_ context.Context, userID int64) ([]participationentity.CodeOption, error) {
	r.UserID = userID
	return r.Options, r.Err
}

func (r *CodeRepository) RequestCode(_ context.Context, userID, dayID, updateID int64) (participationentity.CodeResult, error) {
	r.Requests++
	r.UserID, r.DayID, r.UpdateID = userID, dayID, updateID
	return r.Result, r.Err
}

type RegistrationStub struct {
	Day    func(int64) *registrationentity.RegistrationDay
	Result registrationentity.RegistrationResult
	Err    error
	Calls  int
}

func (s *RegistrationStub) View(_ context.Context, _, dayID int64) (*registrationentity.RegistrationDay, error) {
	if s.Day != nil {
		return s.Day(dayID), s.Err
	}
	return s.Result.Day, s.Err
}

func (s *RegistrationStub) Register(_ context.Context, _, _, _ int64, _ registrationentity.Verification) (registrationentity.RegistrationResult, error) {
	s.Calls++
	return s.Result, s.Err
}

type CallbackAnswer struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
}
type MessageReply struct {
	ChatID      int64                        `json:"chat_id"`
	Text        string                       `json:"text"`
	ReplyMarkup *models.InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}
