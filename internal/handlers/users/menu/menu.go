package menu

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/entity/participation"
	registrationhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users/registration"
	usersession "github.com/letsssgooo/giveaways_bot/internal/handlers/users/session"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
	telegramprotocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

const bannerKeySuffix = "banner"

func Start(ctx context.Context, h *usersession.Session, userID, updateID, chatID int64) error {
	promotion, err := h.Promotion.Current(ctx)
	if err != nil {
		return err
	}
	photo := telegramprotocol.WelcomeBannerRef
	if promotion != nil && strings.TrimSpace(promotion.BannerRef) != "" {
		photo = strings.TrimSpace(promotion.BannerRef)
	}
	// Баннер отправляется отдельно: его постоянная ошибка не блокирует меню.
	if err := h.Enqueue(ctx, userID, updateID, telegramprotocol.MessageKindPhoto, bannerKeySuffix, struct {
		ChatID int64  `json:"chat_id"`
		Photo  string `json:"photo"`
	}{chatID, photo}); err != nil {
		return err
	}
	return h.Reply(ctx, userID, updateID, chatID, userview.WelcomeText, userview.MainKeyboard())
}

func Material(ctx context.Context, h *usersession.Session, userID, updateID, chatID int64, action string) error {
	promotion, err := h.Promotion.Current(ctx)
	if err != nil {
		return err
	}
	text := userview.MissingRulesText
	if action == userview.ActionHow {
		text = fmt.Sprintf(userview.DefaultInstructionsTemplate, participation.MaxFinalChances)
		if promotion != nil && strings.TrimSpace(promotion.Instructions) != "" {
			text = promotion.Instructions
		}
	} else if promotion != nil && strings.TrimSpace(promotion.Rules) != "" {
		text = promotion.Rules
	}
	return h.Reply(ctx, userID, updateID, chatID, text, userview.HomeKeyboard())
}

func ChoosePartner(ctx context.Context, h *usersession.Session, userID, updateID, chatID int64) error {
	partners, err := h.Promotion.AvailablePartners(ctx)
	if err != nil {
		return err
	}
	switch len(partners) {
	case 0:
		return h.Reply(ctx, userID, updateID, chatID, userview.NoPartnersText, userview.HomeKeyboard())
	case 1:
		return registrationhandler.JoinPartner(ctx, h, userID, updateID, chatID, partners[0].ID)
	default:
		keyboard := &models.InlineKeyboardMarkup{}
		for _, partner := range partners {
			keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, []models.InlineKeyboardButton{{
				Text: partner.Name, CallbackData: fmt.Sprintf("%s:%s:%d", userview.PartnerActionGroup, userview.PartnerActionJoin, partner.ID),
			}})
		}
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, userview.HomeKeyboard().InlineKeyboard...)
		return h.Reply(ctx, userID, updateID, chatID, userview.ChoosePartnerText, keyboard)
	}
}
