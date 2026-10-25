package subscription

import (
	"context"
	"net/http"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/api"
)

const apiMethodGetChatMember = "getChatMember"

// Check проверяет подписку только при наличии прав администратора:
// Telegram гарантирует getChatMember для других пользователей при этом условии.
// Ошибки API и неизвестные статусы не считаются отсутствием подписки.
func Check(ctx context.Context, sdk *bot.Bot, chatID, userID int64) (bool, error) {
	if chatID >= 0 || userID <= 0 {
		return false, &api.Error{Method: apiMethodGetChatMember, Code: http.StatusBadRequest, Reason: "invalid subscription chat or user ID"}
	}
	botID := sdk.ID()
	if botID <= 0 {
		return false, &api.Error{Method: apiMethodGetChatMember, Reason: "invalid bot identity"}
	}
	self, err := chatMember(ctx, sdk, chatID, botID)
	if err != nil {
		return false, err
	}
	if self.Type != models.ChatMemberTypeOwner && self.Type != models.ChatMemberTypeAdministrator {
		return false, &api.Error{Method: apiMethodGetChatMember, Reason: "bot must be an administrator in the subscription chat"}
	}
	member, err := chatMember(ctx, sdk, chatID, userID)
	if err != nil {
		return false, err
	}
	switch member.Type {
	case models.ChatMemberTypeOwner, models.ChatMemberTypeAdministrator, models.ChatMemberTypeMember:
		return true, nil
	case models.ChatMemberTypeRestricted:
		return member.Restricted.IsMember, nil
	default: // chatMember уже проверил известный статус: остаются left и kicked.
		return false, nil
	}
}

func chatMember(ctx context.Context, sdk *bot.Bot, chatID, userID int64) (*models.ChatMember, error) {
	member, err := sdk.GetChatMember(ctx, &bot.GetChatMemberParams{ChatID: chatID, UserID: userID})
	if err != nil {
		return nil, api.Wrap(apiMethodGetChatMember, err)
	}
	if memberUserID(member) != userID {
		return nil, &api.Error{Method: apiMethodGetChatMember, Reason: "invalid chat member response"}
	}
	return member, nil
}

func memberUserID(member *models.ChatMember) int64 {
	if member == nil {
		return 0
	}
	var user *models.User
	switch member.Type {
	case models.ChatMemberTypeOwner:
		if member.Owner != nil {
			user = member.Owner.User
		}
	case models.ChatMemberTypeAdministrator:
		if member.Administrator != nil {
			user = &member.Administrator.User
		}
	case models.ChatMemberTypeMember:
		if member.Member != nil {
			user = member.Member.User
		}
	case models.ChatMemberTypeRestricted:
		if member.Restricted != nil {
			user = member.Restricted.User
		}
	case models.ChatMemberTypeLeft:
		if member.Left != nil {
			user = member.Left.User
		}
	case models.ChatMemberTypeBanned:
		if member.Banned != nil {
			user = member.Banned.User
		}
	}
	if user == nil {
		return 0
	}
	return user.ID
}
