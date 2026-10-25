// Package storage содержит доступ к розыгрышам, результатам и получению призов в PostgreSQL.
package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	promotionrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
)

type Repository struct{ tx pgx.Tx }

func New(tx pgx.Tx) *Repository { return &Repository{tx: tx} }

func (r *Repository) LockAdmin(ctx context.Context) error {
	return promotionrepo.New(r.tx).LockAdmin(ctx)
}

const summaryQuery = `SELECT draw.id, coalesce(draw.partner_day_id,0), coalesce(day.partner_id,0),
	draw.kind, coalesce(partner.name,''), coalesce(day.date,promotion.ends_on), draw.scheduled_at,
	draw.performed_at, draw.approved_at, coalesce(draw.approved_by,0),
	(SELECT count(*) FROM draw_participants WHERE draw_id=draw.id),
	(SELECT count(*) FROM draw_results WHERE draw_id=draw.id AND role='winner'),
	(SELECT count(*) FROM draw_results WHERE draw_id=draw.id AND role='reserve'),
	coalesce((SELECT sum(places_count) FROM prizes WHERE draw_id=draw.id),0),
	coalesce(draw.reserve_count,CASE WHEN draw.kind='final' THEN 5
		ELSE coalesce((SELECT sum(places_count) FROM prizes WHERE draw_id=draw.id),0) END)
	FROM draws AS draw JOIN promotions AS promotion ON promotion.id=draw.promotion_id
	LEFT JOIN partner_days AS day ON day.id=draw.partner_day_id
	LEFT JOIN partners AS partner ON partner.id=day.partner_id`

func scanSummary(row pgx.Row) (entity.Summary, error) {
	var summary entity.Summary
	err := row.Scan(&summary.ID, &summary.PartnerDayID, &summary.PartnerID, &summary.Kind,
		&summary.PartnerName, &summary.Date, &summary.ScheduledAt, &summary.PerformedAt,
		&summary.ApprovedAt, &summary.ApprovedBy, &summary.ParticipantCount,
		&summary.WinnerCount, &summary.ReserveCount, &summary.PlacesCount, &summary.ReserveLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return summary, promotionadminentity.ErrNotFound
	}
	if err != nil {
		return summary, fmt.Errorf("read draw: %w", err)
	}
	return summary, nil
}

func (r *Repository) List(ctx context.Context) ([]entity.Summary, error) {
	rows, err := r.tx.Query(ctx, summaryQuery+` ORDER BY draw.scheduled_at DESC, draw.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list draws: %w", err)
	}
	defer rows.Close()
	summaries := make([]entity.Summary, 0)
	for rows.Next() {
		summary, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func (r *Repository) View(ctx context.Context, id int64) (*entity.Result, error) {
	summary, err := scanSummary(r.tx.QueryRow(ctx, summaryQuery+` WHERE draw.id=$1`, id))
	if err != nil {
		return nil, err
	}
	participants, err := r.Participants(ctx, id)
	if err != nil {
		return nil, err
	}
	result := &entity.Result{Summary: summary, Participants: participants, Winners: []entity.Selection{}, Reserves: []entity.Selection{}}
	rows, err := r.tx.Query(ctx, `SELECT result.role,participant.user_id,users.telegram_user_id,
		participant.id,coalesce(result.prize_id,0),result.position,coalesce(result.place_number,0),
		coalesce(prize.units_per_winner,0),coalesce(prize.name,'')
		FROM draw_results AS result JOIN draw_participants AS participant ON participant.id=result.draw_participant_id
		JOIN users ON users.id=participant.user_id LEFT JOIN prizes AS prize ON prize.id=result.prize_id
		WHERE result.draw_id=$1 ORDER BY result.role,result.position`, id)
	if err != nil {
		return nil, fmt.Errorf("read draw selections: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var selection entity.Selection
		if err := rows.Scan(&role, &selection.UserID, &selection.TelegramID, &selection.ParticipantID, &selection.PrizeID,
			&selection.Position, &selection.PlaceNumber, &selection.UnitsPerWinner, &selection.PrizeName); err != nil {
			return nil, err
		}
		if role == "winner" {
			result.Winners = append(result.Winners, selection)
		} else {
			result.Reserves = append(result.Reserves, selection)
		}
	}
	return result, rows.Err()
}

func (r *Repository) Participants(ctx context.Context, id int64) ([]entity.Participant, error) {
	rows, err := r.tx.Query(ctx, `SELECT participant.id,participant.user_id,users.telegram_user_id,participant.weight
		FROM draw_participants AS participant JOIN users ON users.id=participant.user_id
		WHERE participant.draw_id=$1 ORDER BY participant.user_id`, id)
	if err != nil {
		return nil, fmt.Errorf("read draw participants: %w", err)
	}
	defer rows.Close()
	participants := make([]entity.Participant, 0)
	for rows.Next() {
		var participant entity.Participant
		if err := rows.Scan(&participant.ID, &participant.UserID, &participant.TelegramID, &participant.Weight); err != nil {
			return nil, err
		}
		participants = append(participants, participant)
	}
	return participants, rows.Err()
}

// Прием держит SHARE на акции и дне. Финал ждет все операции через акцию,
// ежедневный розыгрыш — операции своего дня. Порядок: акция → день → розыгрыш.
func (r *Repository) Lock(ctx context.Context, id int64) (entity.Summary, error) {
	var kind string
	var dayID int64
	err := r.tx.QueryRow(ctx, `SELECT kind,coalesce(partner_day_id,0) FROM draws WHERE id=$1`, id).Scan(&kind, &dayID)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.Summary{}, promotionadminentity.ErrNotFound
	}
	if err != nil {
		return entity.Summary{}, fmt.Errorf("read draw identity: %w", err)
	}
	lock := "FOR SHARE"
	if kind == "final" {
		lock = "FOR UPDATE"
	}
	var lockedID int64
	if err := r.tx.QueryRow(ctx, `SELECT id FROM promotions WHERE id=1 `+lock).Scan(&lockedID); err != nil {
		return entity.Summary{}, fmt.Errorf("lock draw promotion: %w", err)
	}
	if dayID != 0 {
		if err := r.tx.QueryRow(ctx, `SELECT id FROM partner_days WHERE id=$1 FOR UPDATE`, dayID).Scan(&lockedID); err != nil {
			return entity.Summary{}, fmt.Errorf("lock draw partner day: %w", err)
		}
	}
	summary, err := scanSummary(r.tx.QueryRow(ctx, summaryQuery+` WHERE draw.id=$1 FOR UPDATE OF draw`, id))
	if err == nil && (summary.Kind != kind || summary.PartnerDayID != dayID) {
		return summary, promotionadminentity.ErrConflict
	}
	return summary, err
}
