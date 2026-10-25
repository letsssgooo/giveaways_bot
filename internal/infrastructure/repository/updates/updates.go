// Package updates содержит хранение входящих событий Telegram и позиции их получения.
package updates

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
)

const pollingStateID = 1

type Event struct {
	ID      int64
	Payload json.RawMessage
}

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Offset(ctx context.Context) (int64, error) {
	var offset int64
	if err := r.pool.QueryRow(ctx, `SELECT next_offset FROM telegram_polling_state WHERE id = $1`, pollingStateID).Scan(&offset); err != nil {
		return 0, fmt.Errorf("read Telegram offset: %w", err)
	}
	return offset, nil
}

// Save сохраняет пакет и следующий offset одной транзакцией. fetchedOffset —
// позиция запроса getUpdates: запоздавший пакет не меняет более новую позицию.
// После недели без событий Telegram может начать новую последовательность ID.
func (r *Repository) Save(ctx context.Context, fetchedOffset int64, events []Event) error {
	if fetchedOffset < 0 {
		return errors.New("invalid Telegram polling offset")
	}
	if len(events) == 0 {
		return nil
	}
	ordered := slices.Clone(events)
	slices.SortFunc(ordered, func(a, b Event) int { return cmp.Compare(a.ID, b.ID) })
	if ordered[0].ID < 0 || ordered[len(ordered)-1].ID == math.MaxInt64 {
		return errors.New("invalid Telegram update ID")
	}
	return postgres.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockPollingState(ctx, tx); err != nil {
			return err
		}
		for _, event := range ordered {
			if _, err := tx.Exec(ctx, `INSERT INTO telegram_updates(update_id, payload)
				VALUES ($1, $2) ON CONFLICT (update_id) DO NOTHING`, event.ID, event.Payload); err != nil {
				return fmt.Errorf("save Telegram update: %w", err)
			}
		}
		_, err := tx.Exec(ctx, `UPDATE telegram_polling_state
			SET next_offset = $1 WHERE id = $3 AND next_offset = $2`, ordered[len(ordered)-1].ID+1, fetchedOffset, pollingStateID)
		if err != nil {
			return fmt.Errorf("advance Telegram offset: %w", err)
		}
		return nil
	})
}

// Next читает следующее событие без транзакции и блокировок. Это позволяет
// проверить подписку во внешнем API до атомарной обработки события.
func (r *Repository) Next(ctx context.Context) (*Event, error) {
	var event Event
	err := r.pool.QueryRow(ctx, `SELECT update_id, payload FROM telegram_updates
		WHERE processed_at IS NULL ORDER BY update_id LIMIT 1`).Scan(&event.ID, &event.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read next Telegram update: %w", err)
	}
	return &event, nil
}

// ProcessNext фиксирует результат обработчика вместе с отметкой обработки события.
// Общая блокировка сохраняет порядок update_id, в том числе при нескольких процессах.
func (r *Repository) ProcessNext(ctx context.Context, handle func(context.Context, pgx.Tx, Event) error) (bool, error) {
	processed := false
	err := postgres.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockPollingState(ctx, tx); err != nil {
			return err
		}
		var event Event
		err := tx.QueryRow(ctx, `SELECT update_id, payload FROM telegram_updates
			WHERE processed_at IS NULL ORDER BY update_id LIMIT 1 FOR UPDATE`).Scan(&event.ID, &event.Payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read pending Telegram update: %w", err)
		}
		if err := handle(ctx, tx, event); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE telegram_updates SET processed_at = clock_timestamp()
			WHERE update_id = $1`, event.ID); err != nil {
			return fmt.Errorf("mark Telegram update processed: %w", err)
		}
		processed = true
		return nil
	})
	return processed && err == nil, err
}

func lockPollingState(ctx context.Context, tx pgx.Tx) error {
	var offset int64
	if err := tx.QueryRow(ctx, `SELECT next_offset FROM telegram_polling_state
		WHERE id = $1 FOR UPDATE`, pollingStateID).Scan(&offset); err != nil {
		return fmt.Errorf("lock Telegram polling state: %w", err)
	}
	return nil
}
