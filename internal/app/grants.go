package app

import (
	"context"
	"errors"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	promotionhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
	promotionrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
	usersrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/users"
	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
)

// prepareGrantUsername проверяет ник по числовому ID вне транзакции обработки.
// На подтверждении проверяется тот же получатель, который был показан организатору.
func (a App) prepareGrantUsername(ctx context.Context, db *pgxpool.Pool, update *models.Update) (promotionhandler.GrantVerification, error) {
	var result promotionhandler.GrantVerification
	actor := privateTelegramID(update)
	if actor == 0 || !possibleUsernameGrant(update) {
		return result, nil
	}
	err := postgres.InTx(ctx, db, func(tx pgx.Tx) error {
		access, err := promotionadmin.NewAdmin(promotionrepo.New(tx), a.AdminTelegramIDs).Access(ctx, actor)
		if err != nil || !access.Organizer {
			return err
		}
		users := usersrepo.New(tx)
		user, err := users.FindByTelegramID(ctx, actor)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		dialog, err := dialogs.New(tx).Load(ctx, user.ID)
		if err != nil {
			return err
		}
		username, target, ok := promotionhandler.GrantUsernameRequest(update, dialog)
		if !ok {
			return nil
		}
		result = promotionhandler.GrantVerification{UpdateID: update.ID, TelegramID: target, Username: username}
		if target > 0 {
			return nil
		}
		user, err = users.FindByUsername(ctx, username)
		if errors.Is(err, pgx.ErrNoRows) {
			result.Problem = "не удалось однозначно найти пользователя по нику; попросите его отправить боту /start и повторите ввод или укажите числовой Telegram ID"
			return nil
		}
		result.TelegramID = user.TelegramID
		return err
	})
	if err != nil || result.TelegramID == 0 {
		return result, err
	}
	matches, err := a.Telegram.CheckUsername(ctx, result.TelegramID, result.Username)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	switch {
	case err != nil:
		result.Problem = "Telegram сейчас не позволяет проверить ник; повторите ввод или подтверждение позже"
	case !matches:
		result.Problem = "ник пользователя изменился или недоступен; попросите его отправить боту /start и повторите добавление"
	}
	return result, nil
}

func possibleUsernameGrant(update *models.Update) bool {
	if update.Message != nil {
		return strings.HasPrefix(strings.TrimSpace(update.Message.Text), "@")
	}
	return update.CallbackQuery != nil && strings.HasPrefix(update.CallbackQuery.Data, "admin:confirm:")
}
