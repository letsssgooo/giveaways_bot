// Package dialogs содержит хранение состояний Telegram-диалогов.
package dialogs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Dialog struct {
	State   string
	Payload json.RawMessage
}

type Repository struct {
	tx pgx.Tx
}

func New(tx pgx.Tx) *Repository {
	return &Repository{tx: tx}
}

// Load возвращает только действующий диалог. Истекший сценарий не возобновляется
// после перезапуска и будет заменен следующим Save либо удален через Clear.
func (r *Repository) Load(ctx context.Context, userID int64) (*Dialog, error) {
	var dialog Dialog
	err := r.tx.QueryRow(ctx, `SELECT state, payload FROM dialogs
		WHERE user_id = $1 AND (expires_at IS NULL OR expires_at > clock_timestamp())`, userID).
		Scan(&dialog.State, &dialog.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read dialog: %w", err)
	}
	return &dialog, nil
}

// Save выполняется в транзакции обработки обновления вместе с изменениями
// настроек и постановкой ответа в очередь.
func (r *Repository) Save(ctx context.Context, userID int64, state string, payload json.RawMessage, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("dialog TTL must be positive")
	}
	_, err := r.tx.Exec(ctx, `INSERT INTO dialogs(user_id, state, payload, expires_at)
		VALUES ($1, $2, $3, clock_timestamp() + $4::bigint * interval '1 microsecond')
		ON CONFLICT (user_id) DO UPDATE SET state = EXCLUDED.state, payload = EXCLUDED.payload,
		updated_at = clock_timestamp(), expires_at = EXCLUDED.expires_at`, userID, state, payload, ttl.Microseconds())
	if err != nil {
		return fmt.Errorf("save dialog: %w", err)
	}
	return nil
}

func (r *Repository) Clear(ctx context.Context, userID int64) error {
	if _, err := r.tx.Exec(ctx, `DELETE FROM dialogs WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("clear dialog: %w", err)
	}
	return nil
}
