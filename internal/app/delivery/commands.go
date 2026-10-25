package delivery

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
	promotionrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

const commandsQueryTimeout = 30 * time.Second

func (s Sender) syncCommands(ctx context.Context, payload json.RawMessage) error {
	var request protocol.CommandsRequest
	if json.Unmarshal(payload, &request) != nil || request.ChatID <= 0 {
		// Неверный ID классифицируется клиентом как постоянная ошибка.
		return s.client.SyncCommands(ctx, 0, false)
	}
	var admin bool
	queryCtx, cancel := context.WithTimeout(ctx, commandsQueryTimeout)
	err := postgres.InTx(queryCtx, s.pool, func(tx pgx.Tx) error {
		access, err := promotionadmin.NewAdmin(promotionrepo.New(tx), s.organizerIDs).Access(queryCtx, request.ChatID)
		if err == nil {
			admin = access.Organizer || len(access.PartnerIDs) > 0
		}
		return err
	})
	cancel()
	if err != nil {
		return err
	}
	return s.client.SyncCommands(ctx, request.ChatID, admin)
}
