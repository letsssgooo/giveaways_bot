package chances

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	chancesentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/chances"
)

const promotionID = 1

type Repository struct{ tx pgx.Tx }

func New(tx pgx.Tx) *Repository { return &Repository{tx: tx} }

// Chances читает пропуск без начисления активности и без ограничения истории.
func (r *Repository) Chances(ctx context.Context, userID int64) (chancesentity.Chances, error) {
	var chances chancesentity.Chances
	err := r.tx.QueryRow(ctx, `WITH moment AS MATERIALIZED (SELECT clock_timestamp() AS now)
		SELECT EXISTS (SELECT 1 FROM promotions WHERE id = $1),
			(SELECT count(DISTINCT day.date)
			 FROM partner_days AS day
			 JOIN promotions AS promotion ON promotion.id = day.promotion_id
			 CROSS JOIN moment
			 WHERE promotion.id = $1 AND day.is_enabled
			 AND day.date BETWEEN promotion.starts_on AND promotion.ends_on
			 AND day.closes_at > moment.now)`, promotionID).
		Scan(&chances.Configured, &chances.RemainingDays)
	if err != nil {
		return chancesentity.Chances{}, fmt.Errorf("read remaining participation days: %w", err)
	}
	chances.ActiveDays, err = r.activityDays(ctx, userID)
	if err != nil {
		return chancesentity.Chances{}, err
	}
	chances.Prizes, err = r.finalPrizes(ctx)
	if err != nil {
		return chancesentity.Chances{}, err
	}
	return chances, nil
}

func (r *Repository) activityDays(ctx context.Context, userID int64) ([]chancesentity.ActivityDay, error) {
	rows, err := r.tx.Query(ctx, `WITH interactions AS (
		SELECT issued_date AS date, partner_id FROM promo_codes WHERE user_id = $1
		UNION
		SELECT registration.registered_date AS date, day.partner_id
		FROM registrations AS registration
		JOIN partner_days AS day ON day.id = registration.partner_day_id
		WHERE registration.user_id = $1
	)
	SELECT activity.date, COALESCE(
		array_agg(partner.name ORDER BY partner.name, partner.id) FILTER (WHERE partner.id IS NOT NULL),
		'{}'::text[])
	FROM active_days AS activity
	LEFT JOIN interactions AS interaction ON interaction.date = activity.date
	LEFT JOIN partners AS partner ON partner.id = interaction.partner_id
	WHERE activity.user_id = $1
	GROUP BY activity.date
	ORDER BY activity.date DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("read active days: %w", err)
	}
	defer rows.Close()
	days := make([]chancesentity.ActivityDay, 0)
	for rows.Next() {
		var day chancesentity.ActivityDay
		if err := rows.Scan(&day.Date, &day.Partners); err != nil {
			return nil, fmt.Errorf("scan active day: %w", err)
		}
		days = append(days, day)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read active days: %w", err)
	}
	return days, nil
}

func (r *Repository) finalPrizes(ctx context.Context) ([]chancesentity.FinalPrize, error) {
	rows, err := r.tx.Query(ctx, `SELECT prize.name, prize.places_count, prize.units_per_winner
		FROM prizes AS prize
		JOIN draws AS draw ON draw.id = prize.draw_id
		WHERE draw.promotion_id = $1 AND draw.kind = 'final'
		ORDER BY prize.id`, promotionID)
	if err != nil {
		return nil, fmt.Errorf("read final prizes: %w", err)
	}
	defer rows.Close()
	prizes := make([]chancesentity.FinalPrize, 0)
	for rows.Next() {
		var prize chancesentity.FinalPrize
		if err := rows.Scan(&prize.Name, &prize.PlacesCount, &prize.UnitsPerWinner); err != nil {
			return nil, fmt.Errorf("scan final prize: %w", err)
		}
		prizes = append(prizes, prize)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read final prizes: %w", err)
	}
	return prizes, nil
}
