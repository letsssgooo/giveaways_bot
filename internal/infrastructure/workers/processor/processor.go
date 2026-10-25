package processor

import (
	"context"
	"log/slog"

	workerbase "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers"
)

type Processor struct {
	ProcessNext func(context.Context) (bool, error)
	Logger      *slog.Logger
}

// Run завершает текущую транзакцию при отмене intakeCtx. workCtx ограничивает
// время обработки, если приложение исчерпало срок корректного завершения.
func (p Processor) Run(intakeCtx, workCtx context.Context) error {
	logger := workerbase.LoggerOrDefault(p.Logger)
	failures := 0
	for intakeCtx.Err() == nil && workCtx.Err() == nil {
		ctx, cancel := context.WithTimeout(workCtx, workerbase.DatabaseTimeout)
		processed, err := p.ProcessNext(ctx)
		cancel()
		if intakeCtx.Err() != nil || workCtx.Err() != nil {
			return nil
		}
		if err != nil {
			failures = min(failures+1, workerbase.MaxBackoffAttempt)
			logger.Warn("cannot process pending work")
			if !workerbase.Wait(intakeCtx, workerbase.Backoff(failures)) {
				return nil
			}
			continue
		}
		failures = 0
		if !processed && !workerbase.Wait(intakeCtx, workerbase.IdleInterval) {
			return nil
		}
	}
	return nil
}
