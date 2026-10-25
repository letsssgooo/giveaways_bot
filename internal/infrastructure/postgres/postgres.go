// Package postgres содержит управление соединениями PostgreSQL и общими транзакциями.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	startupTimeout  = 10 * time.Second
	rollbackTimeout = 5 * time.Second
)

// Open создает пул и проверяет соединение. Ошибки не содержат строку подключения.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"

	ctx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("cannot create PostgreSQL pool")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("connect to PostgreSQL: %w", ctx.Err())
		}
		return nil, errors.New("cannot connect to PostgreSQL; check availability and credentials")
	}
	return pool, nil
}

// InTx выполняет сценарий в общей READ COMMITTED транзакции.
// После ошибки сценария, паники или отмены контекста выполняется rollback.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		// После commit транзакция уже закрыта; повторный Rollback безопасен.
		_ = tx.Rollback(rollbackCtx)
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
