package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/letsssgooo/giveaways_bot/internal/app"
	"github.com/letsssgooo/giveaways_bot/internal/config"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
)

const exitFailure = 1

func main() {
	if err := run(); err != nil {
		slog.Error("application failed", "error", err)
		os.Exit(exitFailure)
	}
}

func run() error {
	if len(os.Args) > 1 {
		return errors.New("usage: giveawaysbot")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	client, err := telegram.New(cfg.Telegram.Token, cfg.Telegram.PollTimeout, cfg.Telegram.RequestTimeout)
	if err != nil {
		return err
	}
	return runConfigured(cfg, client)
}

func runConfigured(cfg config.Config, client *telegram.Client) error {
	logger := configureLogger(cfg.LogLevel)
	logger.Info("configuration loaded")

	ctx, stop := signalContext()
	defer stop()

	application := app.App{
		Logger:           logger,
		DatabaseURL:      cfg.Database.URL,
		Telegram:         client,
		ShutdownTimeout:  cfg.ShutdownTimeout,
		RequestTimeout:   cfg.Telegram.RequestTimeout,
		AdminTelegramIDs: cfg.AdminTelegramIDs,
	}
	if err := application.Run(ctx); err != nil {
		return err
	}
	logger.Info("application stopped")
	return nil
}

func configureLogger(level slog.Level) *slog.Logger {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return logger
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}
