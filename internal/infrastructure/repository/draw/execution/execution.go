package execution

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/storage"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
	drawrandom "github.com/letsssgooo/giveaways_bot/internal/entity/draw/random"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
)

type Executor struct {
	tx    pgx.Tx
	store *storage.Repository
}

func New(tx pgx.Tx) *Executor { return &Executor{tx: tx, store: storage.New(tx)} }

// Execute фиксирует весь розыгрыш в транзакции вызывающего кода. Повторный
// вызов возвращает сохраненный результат, не выполняя новый случайный выбор.
func (r *Executor) Execute(ctx context.Context, id int64) (*entity.Result, error) {
	result, _, err := r.ExecuteWithStatus(ctx, id)
	return result, err
}

// ExecuteWithStatus дополнительно сообщает, был ли розыгрыш выполнен этим вызовом.
func (r *Executor) ExecuteWithStatus(ctx context.Context, id int64) (*entity.Result, bool, error) {
	summary, err := r.store.Lock(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if summary.PerformedAt != nil {
		result, err := r.store.View(ctx, id)
		return result, false, err
	}
	var due bool
	err = r.tx.QueryRow(ctx, `WITH moment AS MATERIALIZED(SELECT clock_timestamp() AS now)
		SELECT draw.scheduled_at<=moment.now AND
		CASE WHEN draw.kind='daily' THEN day.closes_at<=moment.now
		ELSE (promotion.ends_on+1)::timestamp AT TIME ZONE 'Europe/Moscow'<=moment.now
			AND NOT EXISTS(SELECT 1 FROM partner_days WHERE promotion_id=draw.promotion_id AND closes_at>moment.now) END
		FROM draws AS draw JOIN promotions AS promotion ON promotion.id=draw.promotion_id
		LEFT JOIN partner_days AS day ON day.id=draw.partner_day_id CROSS JOIN moment WHERE draw.id=$1`, id).Scan(&due)
	if err != nil {
		return nil, false, fmt.Errorf("check draw schedule: %w", err)
	}
	if !due {
		return nil, false, entity.ErrNotDue
	}
	prizes, err := r.prizes(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if len(prizes) == 0 {
		return nil, false, entity.ErrNotReady
	}
	if summary.Kind == "final" && (len(prizes) != 1 || prizes[0].places != 5 || prizes[0].units != 2) {
		return nil, false, fmt.Errorf("%w: финал требует пять мест по два билета", promotionadminentity.ErrConflict)
	}
	if summary.ParticipantCount != 0 {
		return nil, false, fmt.Errorf("%w: незавершенный снимок розыгрыша", promotionadminentity.ErrConflict)
	}
	if err := r.snapshot(ctx, summary); err != nil {
		return nil, false, err
	}
	participants, err := r.store.Participants(ctx, id)
	if err != nil {
		return nil, false, err
	}
	winnerCount := min(summary.PlacesCount, len(participants))
	selected, err := drawrandom.Select(participants, winnerCount+min(summary.ReserveLimit, len(participants)-winnerCount))
	if err != nil {
		return nil, false, err
	}
	if err := r.saveSelections(ctx, id, selected, winnerCount, prizes); err != nil {
		return nil, false, err
	}
	if _, err := r.tx.Exec(ctx, `UPDATE draws SET performed_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		return nil, false, fmt.Errorf("finish draw: %w", err)
	}
	result, err := r.store.View(ctx, id)
	return result, true, err
}

type prize struct {
	id            int64
	places, units int
}

func (r *Executor) prizes(ctx context.Context, id int64) ([]prize, error) {
	rows, err := r.tx.Query(ctx, `SELECT id,places_count,units_per_winner FROM prizes WHERE draw_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("read draw prizes: %w", err)
	}
	defer rows.Close()
	var prizes []prize
	for rows.Next() {
		var p prize
		if err := rows.Scan(&p.id, &p.places, &p.units); err != nil {
			return nil, err
		}
		prizes = append(prizes, p)
	}
	return prizes, rows.Err()
}

func (r *Executor) snapshot(ctx context.Context, draw entity.Summary) error {
	var err error
	if draw.Kind == "daily" {
		_, err = r.tx.Exec(ctx, `INSERT INTO draw_participants(draw_id,draw_kind,user_id,registration_id,partner_day_id,weight)
			SELECT $1,'daily',user_id,id,partner_day_id,1 FROM registrations WHERE partner_day_id=$2 ORDER BY user_id`, draw.ID, draw.PartnerDayID)
	} else {
		_, err = r.tx.Exec(ctx, `INSERT INTO draw_participants(draw_id,draw_kind,user_id,weight)
			SELECT $1,'final',active.user_id,least(count(*),10)::smallint FROM active_days AS active
			JOIN promotions AS promotion ON promotion.id=1
			WHERE active.date BETWEEN promotion.starts_on AND promotion.ends_on GROUP BY active.user_id ORDER BY active.user_id`, draw.ID)
	}
	if err != nil {
		return fmt.Errorf("save draw participant snapshot: %w", err)
	}
	return nil
}

func (r *Executor) saveSelections(ctx context.Context, id int64, selected []entity.Participant, winnerCount int, prizes []prize) error {
	position := 0
	for _, prize := range prizes {
		for place := 1; place <= prize.places && position < winnerCount; place++ {
			participant := selected[position]
			position++
			var resultID int64
			err := r.tx.QueryRow(ctx, `INSERT INTO draw_results(draw_id,draw_participant_id,role,position,prize_id,place_number)
				VALUES($1,$2,'winner',$3,$4,$5) RETURNING id`, id, participant.ID, position, prize.id, place).Scan(&resultID)
			if err != nil {
				return fmt.Errorf("save draw winner: %w", err)
			}
			if _, err := r.tx.Exec(ctx, `INSERT INTO prize_awards(draw_id,draw_result_id,prize_id,place_number)
				VALUES($1,$2,$3,$4)`, id, resultID, prize.id, place); err != nil {
				return fmt.Errorf("assign draw prize: %w", err)
			}
		}
	}
	for i, participant := range selected[winnerCount:] {
		if _, err := r.tx.Exec(ctx, `INSERT INTO draw_results(draw_id,draw_participant_id,role,position)
			VALUES($1,$2,'reserve',$3)`, id, participant.ID, i+1); err != nil {
			return fmt.Errorf("save draw reserve: %w", err)
		}
	}
	return nil
}
