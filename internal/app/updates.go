package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	registrationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation/registration"
	drawhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/draw"
	claimhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/draw/claims"
	claimadminhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/draw/claims/admin"
	notificationhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/notifications"
	promotionhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion"
	reportinghandler "github.com/letsssgooo/giveaways_bot/internal/handlers/reporting"
	usershandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users"
	registrationhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users/registration"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/commands"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
	drawrepository "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw"
	awardrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/awards"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/notifications"
	participationrepository "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/participation"
	registrationrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/participation/registration"
	promotionrepository "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
	reportingrepository "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/reporting"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/updates"
	usersrepository "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/users"
	drawservice "github.com/letsssgooo/giveaways_bot/internal/services/draw"
	claimservice "github.com/letsssgooo/giveaways_bot/internal/services/draw/claims"
	claimadminservice "github.com/letsssgooo/giveaways_bot/internal/services/draw/claims/admin"
	broadcastservice "github.com/letsssgooo/giveaways_bot/internal/services/notifications/broadcasts"
	participationservice "github.com/letsssgooo/giveaways_bot/internal/services/participation"
	registrationservice "github.com/letsssgooo/giveaways_bot/internal/services/participation/registration"
	promotionservice "github.com/letsssgooo/giveaways_bot/internal/services/promotion"
	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	promoimport "github.com/letsssgooo/giveaways_bot/internal/services/promotion/importer"
	reportingservice "github.com/letsssgooo/giveaways_bot/internal/services/reporting"
	usersservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
)

var errNextUpdateChanged = errors.New("next Telegram update changed during preparation")

func (a App) processTelegramUpdate(ctx context.Context, db *pgxpool.Pool, inbox *updates.Repository, username string) (bool, error) {
	event, err := inbox.Next(ctx)
	if err != nil || event == nil {
		return false, err
	}
	var update models.Update
	if err := json.Unmarshal(event.Payload, &update); err != nil || int64(update.ID) != event.ID {
		return false, fmt.Errorf("invalid saved Telegram update %d", event.ID)
	}
	var verification registrationentity.Verification
	if telegramID, dayID, ok := registrationhandler.RegistrationCheck(&update); ok {
		service := registrationservice.NewRegistration(registrationrepo.NewRegistration(db), a.Telegram)
		verification, err = service.Prepare(ctx, telegramID, dayID)
		if err != nil {
			return false, err
		}
	}
	grantVerification, err := a.prepareGrantUsername(ctx, db, &update)
	if err != nil {
		return false, err
	}
	processed, err := inbox.ProcessNext(ctx, func(ctx context.Context, tx pgx.Tx, current updates.Event) error {
		// После внешнего запроса другой процесс мог обработать событие или
		// появилось более раннее. Результат проверки к нему применять нельзя.
		if current.ID != event.ID {
			return errNextUpdateChanged
		}
		if err := a.handleTelegramUpdate(ctx, tx, &update, username, verification, grantVerification); err != nil {
			return err
		}
		return commands.New(tx).Ensure(ctx, privateTelegramID(&update), a.AdminTelegramIDs)
	})
	if errors.Is(err, errNextUpdateChanged) {
		return false, nil
	}
	return processed, err
}

func (a App) handleTelegramUpdate(ctx context.Context, tx pgx.Tx, update *models.Update, username string, verification registrationentity.Verification, grantVerification promotionhandler.GrantVerification) error {
	if from := privateTelegramUser(update); from != nil {
		users := usersrepository.New(tx)
		if _, err := users.Ensure(ctx, from.ID, from.ID); err != nil {
			return err
		}
		if err := users.SaveUsername(ctx, from.ID, from.Username); err != nil {
			return err
		}
	}
	adminRepo := promotionrepository.New(tx)
	admin := promotionadmin.NewAdmin(adminRepo, a.AdminTelegramIDs)
	reportHandler := reportinghandler.New(reportingservice.New(reportingrepository.New(tx), admin),
		usersservice.New(usersrepository.New(tx)), dialogs.New(tx), notifications.New(tx))
	if handled, err := reportHandler.Handle(ctx, update); handled || err != nil {
		return err
	}
	broadcastHandler := notificationhandler.New(broadcastservice.NewBroadcasts(notifications.New(tx), admin), admin, usersservice.New(usersrepository.New(tx)), dialogs.New(tx), notifications.New(tx), username)
	if handled, err := broadcastHandler.Handle(ctx, update); handled || err != nil {
		return err
	}
	awards := awardrepo.NewAwards(tx)
	claimHandler := claimhandler.NewClaims(claimservice.NewClaims(awards), usersservice.New(usersrepository.New(tx)),
		dialogs.New(tx), notifications.New(tx), username)
	if handled, err := claimHandler.Handle(ctx, update); handled || err != nil {
		return err
	}
	claimAdmin := claimadminhandler.NewClaimsAdmin(claimadminservice.NewClaimAdministration(awards, admin, adminRepo), usersservice.New(usersrepository.New(tx)), dialogs.New(tx), notifications.New(tx))
	if handled, err := claimAdmin.Handle(ctx, update); handled || err != nil {
		return err
	}
	drawHandler := drawhandler.New(drawservice.New(drawrepository.New(tx), admin),
		usersservice.New(usersrepository.New(tx)), dialogs.New(tx), notifications.New(tx))
	if handled, err := drawHandler.Handle(ctx, update); handled || err != nil {
		return err
	}
	adminHandler := promotionhandler.New(admin, promoimport.NewImporter(adminRepo, admin), usersservice.New(usersrepository.New(tx)), dialogs.New(tx), notifications.New(tx), username).
		WithGrantVerification(grantVerification)
	if handled, err := adminHandler.Handle(ctx, update); handled || err != nil {
		return err
	}
	handler := usershandler.New(usersservice.New(usersrepository.New(tx)),
		promotionservice.New(promotionrepository.New(tx)), participationservice.New(participationrepository.New(tx)),
		notifications.New(tx), username).
		WithRegistration(registrationservice.NewRegistration(registrationrepo.NewRegistration(tx), nil), verification)
	return handler.Handle(ctx, update)
}

func privateTelegramID(update *models.Update) int64 {
	if user := privateTelegramUser(update); user != nil {
		return user.ID
	}
	return 0
}

func privateTelegramUser(update *models.Update) *models.User {
	if update == nil {
		return nil
	}
	var user *models.User
	var chat models.Chat
	if callback := update.CallbackQuery; callback != nil && callback.ID != "" {
		user = &callback.From
		switch callback.Message.Type {
		case models.MaybeInaccessibleMessageTypeMessage:
			if callback.Message.Message != nil {
				chat = callback.Message.Message.Chat
			}
		case models.MaybeInaccessibleMessageTypeInaccessibleMessage:
			if callback.Message.InaccessibleMessage != nil {
				chat = callback.Message.InaccessibleMessage.Chat
			}
		}
	} else if message := update.Message; message != nil {
		user, chat = message.From, message.Chat
	}
	if user != nil && !user.IsBot && user.ID > 0 && chat.Type == models.ChatTypePrivate && chat.ID == user.ID {
		return user
	}
	return nil
}
