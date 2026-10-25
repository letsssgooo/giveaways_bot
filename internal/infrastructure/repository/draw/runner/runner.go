package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/execution"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
)

type Runner struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Runner { return &Runner{pool: pool} }

// RunNext каждый раз читает расписание из БД, поэтому пропущенные при остановке
// приложения розыгрыши выполняются после запуска. Долгого таймера в памяти нет.
func (r *Runner) RunNext(ctx context.Context) (bool, error) {
	performed := false
	err := postgres.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `WITH moment AS MATERIALIZED(SELECT clock_timestamp() AS now)
			SELECT draw.id FROM draws AS draw JOIN promotions AS promotion ON promotion.id=draw.promotion_id
			LEFT JOIN partner_days AS day ON day.id=draw.partner_day_id CROSS JOIN moment
			WHERE draw.performed_at IS NULL AND draw.scheduled_at<=moment.now
			AND EXISTS(SELECT 1 FROM prizes WHERE draw_id=draw.id)
			AND CASE WHEN draw.kind='daily' THEN day.closes_at<=moment.now
			ELSE (promotion.ends_on+1)::timestamp AT TIME ZONE 'Europe/Moscow'<=moment.now
				AND NOT EXISTS(SELECT 1 FROM partner_days WHERE promotion_id=draw.promotion_id AND closes_at>moment.now) END
			ORDER BY draw.scheduled_at,draw.id LIMIT 1`).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("find due draw: %w", err)
		}
		_, performed, err = execution.New(tx).ExecuteWithStatus(ctx, id)
		// Расписание могли перенести между поиском и получением блокировок.
		if errors.Is(err, entity.ErrNotDue) {
			return nil
		}
		return err
	})
	return performed && err == nil, err
}
