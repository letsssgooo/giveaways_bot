// Package workers принимает обновления Telegram, обрабатывает сохраненные события
// и доставляет сообщения из очереди PostgreSQL.
package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
)

const (
	DatabaseTimeout          = 30 * time.Second
	IdleInterval             = 100 * time.Millisecond
	InitialRetryDelay        = time.Second
	MaxRetryDelay            = 30 * time.Second
	MaxBackoffAttempt        = 6
	MaxSendRequestsPerSecond = 20
	MinSendInterval          = time.Second / MaxSendRequestsPerSecond
)

func LoggerOrDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

func Backoff(attempt int) time.Duration {
	if attempt <= 1 {
		return InitialRetryDelay
	}
	if attempt >= MaxBackoffAttempt {
		return MaxRetryDelay
	}
	return InitialRetryDelay << (attempt - 1)
}

func RetryDelay(attempt int, err error) time.Duration {
	return max(Backoff(attempt), telegram.RetryAfter(err))
}

func Wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
