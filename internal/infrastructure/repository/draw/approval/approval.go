package approval

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/storage"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
)

type Approver struct {
	tx    pgx.Tx
	store *storage.Repository
}

func New(tx pgx.Tx) *Approver { return &Approver{tx: tx, store: storage.New(tx)} }

// Approve вызывается сервисом после проверки прав под LockAdmin в этой же
// транзакции. Здесь сохраняются утверждение и задание будущей отправки.
func (r *Approver) Approve(ctx context.Context, actor, drawID int64) (*entity.Result, error) {
	summary, err := r.store.Lock(ctx, drawID)
	if err != nil {
		return nil, err
	}
	if summary.PerformedAt == nil {
		return nil, entity.ErrNotReady
	}
	if summary.ApprovedAt != nil {
		return r.store.View(ctx, drawID)
	}
	var userID int64
	err = r.tx.QueryRow(ctx, `SELECT id FROM users WHERE telegram_user_id=$1`, actor).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, promotionadminentity.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read draw approver: %w", err)
	}
	if _, err := r.tx.Exec(ctx, `UPDATE draws SET approved_at=clock_timestamp(),approved_by=$2 WHERE id=$1`, drawID, userID); err != nil {
		return nil, fmt.Errorf("approve draw: %w", err)
	}
	if _, err := r.tx.Exec(ctx, `INSERT INTO background_jobs(deduplication_key,kind,payload,available_at)
		SELECT 'draw:'||id||':notify_winners','notify_draw_winners',jsonb_build_object('draw_id',id),approved_at
		FROM draws WHERE id=$1 ON CONFLICT(deduplication_key) DO NOTHING`, drawID); err != nil {
		return nil, fmt.Errorf("schedule draw winner notifications: %w", err)
	}
	return r.store.View(ctx, drawID)
}
