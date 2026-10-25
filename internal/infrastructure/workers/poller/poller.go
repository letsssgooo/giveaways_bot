package poller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/updates"
	workerbase "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers"
)

type UpdateFetcher interface {
	Fetch(context.Context, int64) ([]models.Update, error)
}

type UpdateStore interface {
	Offset(context.Context) (int64, error)
	Save(context.Context, int64, []updates.Event) error
}

type Poller struct {
	Fetcher UpdateFetcher
	Store   UpdateStore
	Logger  *slog.Logger
}

// Run прерывает long polling при остановке приема. Уже полученный пакет
// сохраняется с workCtx; новый offset читается только после успешного сохранения.
func (p Poller) Run(intakeCtx, workCtx context.Context) error {
	logger := workerbase.LoggerOrDefault(p.Logger)
	failures := 0
	for intakeCtx.Err() == nil && workCtx.Err() == nil {
		dbCtx, cancel := context.WithTimeout(intakeCtx, workerbase.DatabaseTimeout)
		offset, err := p.Store.Offset(dbCtx)
		cancel()
		if err != nil {
			if intakeCtx.Err() != nil || workCtx.Err() != nil {
				return nil
			}
			failures = min(failures+1, workerbase.MaxBackoffAttempt)
			logger.Warn("cannot read Telegram polling offset")
			if !workerbase.Wait(intakeCtx, workerbase.Backoff(failures)) {
				return nil
			}
			continue
		}
		if intakeCtx.Err() != nil || workCtx.Err() != nil {
			return nil
		}
		batch, err := p.Fetcher.Fetch(intakeCtx, offset)
		if err != nil {
			if intakeCtx.Err() != nil || workCtx.Err() != nil {
				return nil
			}
			if telegram.IsFatal(err) {
				return errors.New("telegram polling stopped: authentication failure or polling conflict")
			}
			failures = min(failures+1, workerbase.MaxBackoffAttempt)
			logger.Warn("Telegram polling failed", "retry_after", workerbase.RetryDelay(failures, err))
			if !workerbase.Wait(intakeCtx, workerbase.RetryDelay(failures, err)) {
				return nil
			}
			continue
		}
		if len(batch) == 0 {
			failures = 0
			if !workerbase.Wait(intakeCtx, workerbase.IdleInterval) {
				return nil
			}
			continue
		}
		events := make([]updates.Event, 0, len(batch))
		for _, update := range batch {
			payload, err := json.Marshal(update)
			if err != nil {
				return errors.New("cannot encode Telegram update")
			}
			events = append(events, updates.Event{ID: update.ID, Payload: payload})
		}
		if !p.persist(workCtx, offset, events, logger) {
			return nil
		}
		failures = 0
	}
	return nil
}

func (p Poller) persist(ctx context.Context, fetchedOffset int64, events []updates.Event, logger *slog.Logger) bool {
	failures := 0
	for ctx.Err() == nil {
		dbCtx, cancel := context.WithTimeout(ctx, workerbase.DatabaseTimeout)
		err := p.Store.Save(dbCtx, fetchedOffset, events)
		cancel()
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		failures = min(failures+1, workerbase.MaxBackoffAttempt)
		logger.Warn("cannot persist Telegram updates", "count", len(events))
		if !workerbase.Wait(ctx, workerbase.Backoff(failures)) {
			return false
		}
	}
	return false
}
