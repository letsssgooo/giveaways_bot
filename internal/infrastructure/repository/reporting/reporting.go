// Package reporting содержит запросы статистики и выгрузок из PostgreSQL.
package reporting

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/reporting/exports"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/reporting"
	promotionrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
)

type Repository struct {
	*exports.Repository
	tx pgx.Tx
}

func New(tx pgx.Tx) *Repository { return &Repository{Repository: exports.New(tx), tx: tx} }

func (r *Repository) LockAdmin(ctx context.Context) error {
	return promotionrepo.New(r.tx).LockAdmin(ctx)
}

func (r *Repository) Partners(ctx context.Context, scope entity.Scope) ([]entity.Partner, error) {
	rows, err := r.tx.Query(ctx, `SELECT id,name FROM partners WHERE $1 OR id=ANY($2::bigint[]) ORDER BY name,id`, scope.AllPartners, scope.PartnerIDs)
	if err != nil {
		return nil, fmt.Errorf("read reporting partners: %w", err)
	}
	defer rows.Close()
	partners := make([]entity.Partner, 0)
	for rows.Next() {
		var partner entity.Partner
		if err := rows.Scan(&partner.ID, &partner.Name); err != nil {
			return nil, fmt.Errorf("scan reporting partner: %w", err)
		}
		partners = append(partners, partner)
	}
	return partners, rows.Err()
}

func (r *Repository) Stats(ctx context.Context, scope entity.Scope) (entity.Stats, error) {
	stats := entity.Stats{GlobalUsersVisible: scope.IncludeUsers, NewUsers: []entity.DailyUsers{}}
	partners, err := r.partnerStats(ctx, scope)
	if err != nil {
		return entity.Stats{}, err
	}
	stats.Partners = partners
	for _, partner := range partners {
		stats.PromoRequests += partner.Requests
		stats.PromoIssued += partner.Issued
		stats.PromoRemaining += partner.Remaining
		stats.Registrations += partner.Registrations
	}
	stats.Days, err = r.dayStats(ctx, scope)
	if err != nil {
		return entity.Stats{}, err
	}
	if scope.IncludeUsers {
		stats.NewUsers, err = r.newUsers(ctx)
		if err != nil {
			return entity.Stats{}, err
		}
		for _, day := range stats.NewUsers {
			stats.TotalUsers += day.Count
		}
	}
	return stats, nil
}

func (r *Repository) partnerStats(ctx context.Context, scope entity.Scope) ([]entity.PartnerStats, error) {
	// Каждая таблица агрегируется отдельно: соединение исходных запросов,
	// кодов и заявок перемножило бы строки и завысило показатели.
	rows, err := r.tx.Query(ctx, `WITH requests AS (
		SELECT day.partner_id,count(*) AS count FROM promo_code_requests AS request
		JOIN partner_days AS day ON day.id=request.partner_day_id GROUP BY day.partner_id
	), codes AS (
		SELECT partner_id,count(*) FILTER(WHERE user_id IS NOT NULL) AS issued,
			count(*) FILTER(WHERE user_id IS NULL) AS remaining FROM promo_codes GROUP BY partner_id
	), registrations_count AS (
		SELECT day.partner_id,count(*) AS count FROM registrations AS registration
		JOIN partner_days AS day ON day.id=registration.partner_day_id GROUP BY day.partner_id
	)
	SELECT partner.id,partner.name,coalesce(requests.count,0),coalesce(codes.issued,0),
		coalesce(codes.remaining,0),coalesce(registrations_count.count,0)
	FROM partners AS partner
	LEFT JOIN requests ON requests.partner_id=partner.id
	LEFT JOIN codes ON codes.partner_id=partner.id
	LEFT JOIN registrations_count ON registrations_count.partner_id=partner.id
	WHERE $1 OR partner.id=ANY($2::bigint[]) ORDER BY partner.name,partner.id`, scope.AllPartners, scope.PartnerIDs)
	if err != nil {
		return nil, fmt.Errorf("read partner statistics: %w", err)
	}
	defer rows.Close()
	stats := make([]entity.PartnerStats, 0)
	for rows.Next() {
		var partner entity.PartnerStats
		if err := rows.Scan(&partner.ID, &partner.Name, &partner.Requests, &partner.Issued, &partner.Remaining, &partner.Registrations); err != nil {
			return nil, fmt.Errorf("scan partner statistics: %w", err)
		}
		stats = append(stats, partner)
	}
	return stats, rows.Err()
}

func (r *Repository) dayStats(ctx context.Context, scope entity.Scope) ([]entity.DayStats, error) {
	rows, err := r.tx.Query(ctx, `SELECT day.partner_id,day.id,partner.name,day.date,count(registration.id)
		FROM partner_days AS day JOIN partners AS partner ON partner.id=day.partner_id
		LEFT JOIN registrations AS registration ON registration.partner_day_id=day.id
		WHERE $1 OR partner.id=ANY($2::bigint[])
		GROUP BY day.id,partner.name ORDER BY day.date,partner.name,day.id`, scope.AllPartners, scope.PartnerIDs)
	if err != nil {
		return nil, fmt.Errorf("read daily participant statistics: %w", err)
	}
	defer rows.Close()
	stats := make([]entity.DayStats, 0)
	for rows.Next() {
		var day entity.DayStats
		if err := rows.Scan(&day.PartnerID, &day.PartnerDayID, &day.PartnerName, &day.Date, &day.Registrations); err != nil {
			return nil, fmt.Errorf("scan daily participant statistics: %w", err)
		}
		stats = append(stats, day)
	}
	return stats, rows.Err()
}

func (r *Repository) newUsers(ctx context.Context) ([]entity.DailyUsers, error) {
	rows, err := r.tx.Query(ctx, `SELECT (started_at AT TIME ZONE 'Europe/Moscow')::date AS date,count(*)
		FROM users GROUP BY date ORDER BY date`)
	if err != nil {
		return nil, fmt.Errorf("read new user statistics: %w", err)
	}
	defer rows.Close()
	days := make([]entity.DailyUsers, 0)
	for rows.Next() {
		var day entity.DailyUsers
		if err := rows.Scan(&day.Date, &day.Count); err != nil {
			return nil, fmt.Errorf("scan new user statistics: %w", err)
		}
		days = append(days, day)
	}
	return days, rows.Err()
}
