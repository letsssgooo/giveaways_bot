package session

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-telegram/bot/models"
	registrationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation/registration"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
	participationservice "github.com/letsssgooo/giveaways_bot/internal/services/participation"
	promotionservice "github.com/letsssgooo/giveaways_bot/internal/services/promotion"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	telegramprotocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type Outbox interface {
	Enqueue(ctx context.Context, userID, updateID int64, kind string, payload json.RawMessage, key string) error
}

type Registration interface {
	View(context.Context, int64, int64) (*registrationentity.RegistrationDay, error)
	Register(context.Context, int64, int64, int64, registrationentity.Verification) (registrationentity.RegistrationResult, error)
}

// Session содержит зависимости текущего Telegram-события и общую отправку ответов.
type Session struct {
	Users         *userservice.Service
	Promotion     *promotionservice.Service
	Participation *participationservice.Service
	Registration  Registration
	Verification  registrationentity.Verification
	Outbox        Outbox
	BotUsername   string
}

const replyKeySuffix = "reply"

func PrivateUser(user *models.User, chat models.Chat) bool {
	return user != nil && user.ID > 0 && !user.IsBot && chat.Type == models.ChatTypePrivate && chat.ID == user.ID
}

type messageReply struct {
	ChatID      int64                        `json:"chat_id"`
	Text        string                       `json:"text"`
	ReplyMarkup *models.InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

func (h *Session) Reply(ctx context.Context, userID, updateID, chatID int64, text string, keyboard *models.InlineKeyboardMarkup) error {
	parts := userview.SplitMessage(text)
	for i, part := range parts {
		suffix := replyKeySuffix
		if i > 0 {
			suffix = fmt.Sprintf("%s:%d", replyKeySuffix, i)
		}
		var markup *models.InlineKeyboardMarkup
		if i == len(parts)-1 {
			markup = keyboard
		}
		if err := h.Enqueue(ctx, userID, updateID, telegramprotocol.MessageKindText, suffix, messageReply{
			ChatID: chatID, Text: part, ReplyMarkup: markup,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h *Session) Enqueue(ctx context.Context, userID, updateID int64, kind, suffix string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Telegram response: %w", err)
	}
	key := fmt.Sprintf("telegram:update:%d:%s", updateID, suffix)
	if err := h.Outbox.Enqueue(ctx, userID, updateID, kind, encoded, key); err != nil {
		return fmt.Errorf("enqueue Telegram response: %w", err)
	}
	return nil
}
