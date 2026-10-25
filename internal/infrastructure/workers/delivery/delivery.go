package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/notifications"
	workerbase "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers"
)

type MessageSender interface {
	Send(context.Context, string, json.RawMessage) error
}

type DeliveryQueue interface {
	Claim(context.Context, time.Duration) (*notifications.Message, error)
	Sent(context.Context, int64, string) error
	Retry(context.Context, int64, string, time.Duration, bool) error
	RateLimited(context.Context, int64, string, time.Duration) error
}

type Delivery struct {
	Sender        MessageSender
	Queue         DeliveryQueue
	LeaseDuration time.Duration
	Logger        *slog.Logger
}

// Run прекращает брать сообщения после отмены intakeCtx. Текущая отправка и
// запись результата используют workCtx; при его отмене аренда остается до рестарта.
func (d Delivery) Run(intakeCtx, workCtx context.Context) error {
	if d.LeaseDuration <= 0 {
		return errors.New("delivery lease duration must be positive")
	}
	logger := workerbase.LoggerOrDefault(d.Logger)
	failures := 0
	var nextSendAt time.Time
	for intakeCtx.Err() == nil && workCtx.Err() == nil {
		if delay := time.Until(nextSendAt); delay > 0 && !workerbase.Wait(intakeCtx, delay) {
			return nil
		}
		claimCtx, cancel := context.WithTimeout(intakeCtx, workerbase.DatabaseTimeout)
		message, err := d.Queue.Claim(claimCtx, d.LeaseDuration)
		cancel()
		if err != nil {
			if intakeCtx.Err() != nil || workCtx.Err() != nil {
				return nil
			}
			failures = min(failures+1, workerbase.MaxBackoffAttempt)
			logger.Warn("cannot claim Telegram message")
			if !workerbase.Wait(intakeCtx, workerbase.Backoff(failures)) {
				return nil
			}
			continue
		}
		if message == nil {
			failures = 0
			if !workerbase.Wait(intakeCtx, workerbase.IdleInterval) {
				return nil
			}
			continue
		}
		nextSendAt = time.Now().Add(workerbase.MinSendInterval)
		failures, err = d.deliverAndWait(intakeCtx, workCtx, message, failures, logger)
		if err != nil {
			return err
		}
	}
	return nil
}

func (d Delivery) deliverAndWait(intakeCtx, workCtx context.Context, message *notifications.Message, failures int, logger *slog.Logger) (int, error) {
	fatal, pause, saveErr := d.deliver(workCtx, message, logger)
	if fatal {
		if saveErr != nil {
			logger.Warn("cannot release Telegram message after fatal API error", "message_id", message.ID)
		}
		return failures, errors.New("telegram delivery stopped: authentication failure or API conflict")
	}
	if workCtx.Err() != nil || intakeCtx.Err() != nil {
		return failures, nil
	}
	switch {
	case errors.Is(saveErr, notifications.ErrLeaseLost):
		logger.Warn("Telegram message lease changed", "message_id", message.ID)
	case saveErr != nil:
		failures = min(failures+1, workerbase.MaxBackoffAttempt)
		logger.Warn("cannot persist Telegram delivery result", "message_id", message.ID)
		pause = max(workerbase.Backoff(failures), pause)
	default:
		failures = 0
	}
	if pause > 0 {
		workerbase.Wait(intakeCtx, pause)
	}
	return failures, nil
}

func (d Delivery) deliver(ctx context.Context, message *notifications.Message, logger *slog.Logger) (fatal bool, pause time.Duration, saveErr error) {
	if ctx.Err() != nil {
		return false, 0, nil
	}
	err := d.Sender.Send(ctx, message.Kind, message.Payload)
	if ctx.Err() != nil {
		return false, 0, nil
	}
	dbCtx, cancel := context.WithTimeout(ctx, workerbase.DatabaseTimeout)
	defer cancel()
	if err == nil {
		return false, 0, d.Queue.Sent(dbCtx, message.ID, message.AttemptToken)
	}
	permanent := telegram.IsPermanent(err)
	fatal = telegram.IsFatal(err)
	delay := workerbase.RetryDelay(message.Attempts, err)
	logger.Warn("Telegram message send failed", "message_id", message.ID,
		"attempt", message.Attempts, "permanent", permanent, "fatal", fatal, "retry_after", delay)
	if telegram.IsRateLimited(err) {
		return false, delay, d.Queue.RateLimited(dbCtx, message.ID, message.AttemptToken, delay)
	}
	return fatal, 0, d.Queue.Retry(dbCtx, message.ID, message.AttemptToken, delay, permanent)
}
