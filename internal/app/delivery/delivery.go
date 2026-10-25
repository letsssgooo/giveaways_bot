package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	reportingentity "github.com/letsssgooo/giveaways_bot/internal/entity/reporting"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/sender"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
	promotionrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
	reportingrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/reporting"
	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	reportingservice "github.com/letsssgooo/giveaways_bot/internal/services/reporting"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

// Sender формирует CSV только перед отправкой. Outbox содержит параметры,
// поэтому отзыв доступа до очередной попытки не раскрывает сохраненный файл.
type Sender struct {
	ordinary     *sender.Sender
	client       *telegram.Client
	pool         *pgxpool.Pool
	organizerIDs []int64
}

// New создает отправителя сообщений, отчетов и меню команд.
func New(client *telegram.Client, pool *pgxpool.Pool, organizerIDs []int64) *Sender {
	return &Sender{ordinary: sender.New(client), client: client, pool: pool, organizerIDs: organizerIDs}
}

func (s Sender) Send(ctx context.Context, kind string, payload json.RawMessage) error {
	if kind == protocol.MessageKindCommands {
		return s.syncCommands(ctx, payload)
	}
	if kind != protocol.MessageKindReport {
		return s.ordinary.Send(ctx, kind, payload)
	}
	var request protocol.ReportRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.ActorID <= 0 || request.ChatID != request.ActorID || request.PartnerID < 0 ||
		(request.Kind != "participants" && request.Kind != "winners") {
		return telegram.InvalidReportError()
	}
	var document reportingentity.Document
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := postgres.InTx(queryCtx, s.pool, func(tx pgx.Tx) error {
		service := reportingservice.New(reportingrepo.New(tx), promotionadmin.NewAdmin(promotionrepo.New(tx), s.organizerIDs))
		var err error
		document, err = service.Export(queryCtx, request.ActorID, request.PartnerID, request.Kind)
		return err
	})
	cancel()
	if errors.Is(err, promotionadminentity.ErrForbidden) || errors.Is(err, promotionadminentity.ErrNotFound) || errors.Is(err, promotionadminentity.ErrInvalid) {
		return telegram.InvalidReportError()
	}
	if errors.Is(err, reportingentity.ErrExportTooLarge) {
		message, _ := json.Marshal(struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}{request.ChatID, "Выгрузка слишком большая для одного файла. Выберите отдельного партнера в разделе статистики и повторите запрос."})
		return s.client.Send(ctx, protocol.MessageKindText, message)
	}
	if err != nil {
		return err
	}
	return s.client.SendDocument(ctx, request.ChatID, document.Filename, document.Data, document.Caption)
}
