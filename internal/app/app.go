// Package app содержит создание компонентов и связывание зависимостей приложения.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	appdelivery "github.com/letsssgooo/giveaways_bot/internal/app/delivery"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/fetcher"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/commands"
	awardrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/awards"
	drawrunner "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/runner"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/notifications"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/updates"
	deliveryworker "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/delivery"
	drawsworker "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/draws"
	pollerworker "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/poller"
	processorworker "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/processor"
	awardmessages "github.com/letsssgooo/giveaways_bot/internal/services/notifications/awards"
)

const (
	applicationComponentCount = 5
	deliveryLeaseMargin       = time.Minute
)

// App содержит зависимости приложения.
type App struct {
	Logger           *slog.Logger
	DatabaseURL      string
	Telegram         *telegram.Client
	ShutdownTimeout  time.Duration
	RequestTimeout   time.Duration
	AdminTelegramIDs []int64
}

// Run подключает БД и Telegram, запускает прием, обработку и отправку событий.
func (a App) Run(ctx context.Context) error {
	if a.Telegram == nil {
		return errors.New("telegram client is required")
	}
	defer a.Telegram.Close()
	if a.ShutdownTimeout <= 0 || a.RequestTimeout <= 0 {
		return errors.New("application timeouts must be positive")
	}
	logger := a.Logger
	if logger == nil {
		logger = slog.Default()
	}

	db, err := postgres.Open(ctx, a.DatabaseURL)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer db.Close()
	if _, err := updates.New(db).Offset(ctx); err != nil {
		return startupError(ctx, err)
	}

	me, err := a.Telegram.GetMe(ctx)
	if err != nil {
		return startupError(ctx, err)
	}
	if err := a.Telegram.Configure(ctx); err != nil {
		return startupError(ctx, err)
	}
	if err := postgres.InTx(ctx, db, func(tx pgx.Tx) error {
		return commands.New(tx).Startup(ctx, a.AdminTelegramIDs)
	}); err != nil {
		return startupError(ctx, err)
	}
	return a.runComponents(ctx, db, me.Username, logger)
}

func startupError(ctx context.Context, err error) error {
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (a App) runComponents(ctx context.Context, db *pgxpool.Pool, username string, logger *slog.Logger) error {
	intakeCtx, stopIntake := context.WithCancel(ctx)
	defer stopIntake()
	workCtx, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWork()
	results := make(chan error, applicationComponentCount)

	inbox := updates.New(db)
	poller := pollerworker.Poller{Fetcher: fetcher.New(a.Telegram), Store: inbox, Logger: logger.With("component", "fetcher")}
	processor := processorworker.Processor{ProcessNext: func(ctx context.Context) (bool, error) {
		return a.processTelegramUpdate(ctx, db, inbox, username)
	}, Logger: logger.With("component", "processor")}
	delivery := deliveryworker.Delivery{
		Sender: appdelivery.New(a.Telegram, db, a.AdminTelegramIDs), Queue: notifications.NewQueue(db),
		LeaseDuration: a.RequestTimeout + deliveryLeaseMargin, Logger: logger.With("component", "sender"),
	}
	draws := drawsworker.DrawScheduler{ProcessNext: drawrunner.New(db).RunNext, Logger: logger.With("component", "draws")}
	prizes := processorworker.Processor{
		ProcessNext: awardrepo.NewAwardProcessor(db, awardmessages.AwardMessages).ProcessNext,
		Logger:      logger.With("component", "prizes"),
	}

	go func() { results <- componentError("fetcher", poller.Run(intakeCtx, workCtx)) }()
	go func() { results <- componentError("processor", processor.Run(intakeCtx, workCtx)) }()
	go func() { results <- componentError("sender", delivery.Run(intakeCtx, workCtx)) }()
	go func() { results <- componentError("draw scheduler", draws.Run(intakeCtx, workCtx)) }()
	go func() { results <- componentError("prizes", prizes.Run(intakeCtx, workCtx)) }()
	remaining := applicationComponentCount
	logger.Info("application started", "bot_username", username)
	var problems []error
	select {
	case <-ctx.Done():
	case err := <-results:
		remaining--
		if err == nil && ctx.Err() == nil {
			err = errors.New("telegram component stopped unexpectedly")
		}
		problems = append(problems, err)
	}
	stopIntake()
	logger.Info("stopping application", "shutdown_timeout", a.ShutdownTimeout)
	return errors.Join(append(problems, waitForComponents(results, remaining, a.ShutdownTimeout, cancelWork))...)
}

func componentError(name string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("%s: %w", name, err)
}

func waitForComponents(results <-chan error, remaining int, timeout time.Duration, cancelWork context.CancelFunc) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	deadline := timer.C
	var problems []error
	for remaining > 0 {
		select {
		case err := <-results:
			remaining--
			problems = append(problems, err)
		case <-deadline:
			problems = append(problems, fmt.Errorf("wait for Telegram operations: %w", context.DeadlineExceeded))
			cancelWork()
			deadline = nil
		}
	}
	// Все операции закончили использовать соединения; Run может закрыть пул.
	return errors.Join(problems...)
}
