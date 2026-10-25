// Package promotion содержит доступ к настройкам акции и партнерам в PostgreSQL.
package promotion

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion/chances"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion/importer"
)

const (
	promotionID       = 1
	promotionTimeZone = "Europe/Moscow"
)

type adminRepository = admin.Repository
type chancesRepository = chances.Repository
type importRepository = importer.Repository

type Repository struct {
	*adminRepository
	*chancesRepository
	*importRepository
	tx pgx.Tx
}

func New(tx pgx.Tx) *Repository {
	return &Repository{adminRepository: admin.New(tx), chancesRepository: chances.New(tx), importRepository: importer.New(tx), tx: tx}
}

func (r *Repository) Current(ctx context.Context) (*entity.Promotion, error) {
	var promotion entity.Promotion
	err := r.tx.QueryRow(ctx, `SELECT name, rules, participation_instructions, banner_ref, starts_on, ends_on
		FROM promotions WHERE id = $1`, promotionID).Scan(&promotion.Name, &promotion.Rules,
		&promotion.Instructions, &promotion.BannerRef, &promotion.StartsOn, &promotion.EndsOn)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read promotion: %w", err)
	}
	return &promotion, nil
}

// AvailablePartners проверяет прием по времени обработки в БД, а не начала
// транзакции. Календарная дата всегда определяется в московском часовом поясе.
func (r *Repository) AvailablePartners(ctx context.Context) ([]entity.PartnerDay, error) {
	rows, err := r.tx.Query(ctx, `WITH moment AS MATERIALIZED (SELECT clock_timestamp() AS now)
		SELECT day.id, day.partner_id, partner.name, day.date, day.opens_at, day.closes_at
		FROM partner_days AS day
		JOIN partners AS partner ON partner.id = day.partner_id
		JOIN promotions AS promotion ON promotion.id = day.promotion_id
		CROSS JOIN moment
		WHERE day.is_enabled AND day.date = (moment.now AT TIME ZONE $1)::date
		AND day.date BETWEEN promotion.starts_on AND promotion.ends_on
		AND day.opens_at <= moment.now AND moment.now < day.closes_at
		ORDER BY partner.name, day.id`, promotionTimeZone)
	if err != nil {
		return nil, fmt.Errorf("read available partners: %w", err)
	}
	defer rows.Close()
	partners := make([]entity.PartnerDay, 0)
	for rows.Next() {
		var partner entity.PartnerDay
		if err := rows.Scan(&partner.ID, &partner.PartnerID, &partner.Name,
			&partner.Date, &partner.OpensAt, &partner.ClosesAt); err != nil {
			return nil, fmt.Errorf("scan available partner: %w", err)
		}
		partners = append(partners, partner)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read available partners: %w", err)
	}
	return partners, nil
}
