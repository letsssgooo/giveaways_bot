package draws

import (
	"context"
	"log/slog"
	"time"

	workerbase "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers"
)

const drawIdleInterval = time.Second

type DrawScheduler struct {
	ProcessNext func(context.Context) (bool, error)
	Logger      *slog.Logger
}

// Run проверяет расписание и завершает текущий розыгрыш при остановке приема.
// workCtx позволяет прервать транзакцию по истечении срока graceful shutdown.
func (s DrawScheduler) Run(intakeCtx, workCtx context.Context) error {
	logger := workerbase.LoggerOrDefault(s.Logger)
	waitCtx, stopWaiting := context.WithCancel(intakeCtx)
	defer stopWaiting()
	stopWorkWatch := context.AfterFunc(workCtx, stopWaiting)
	defer stopWorkWatch()
	failures := 0
	for intakeCtx.Err() == nil && workCtx.Err() == nil {
		ctx, cancel := context.WithTimeout(workCtx, workerbase.DatabaseTimeout)
		processed, err := s.ProcessNext(ctx)
		cancel()
		if intakeCtx.Err() != nil || workCtx.Err() != nil {
			return nil
		}
		if err != nil {
			failures = min(failures+1, workerbase.MaxBackoffAttempt)
			logger.Warn("cannot process scheduled draw")
			if !workerbase.Wait(waitCtx, workerbase.Backoff(failures)) {
				return nil
			}
			continue
		}
		failures = 0
		if !processed && !workerbase.Wait(waitCtx, drawIdleInterval) {
			return nil
		}
	}
	return nil
}
