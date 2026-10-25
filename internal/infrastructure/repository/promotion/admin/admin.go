package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/commands"
)

type Repository struct{ tx pgx.Tx }

func New(tx pgx.Tx) *Repository { return &Repository{tx: tx} }

// Административные изменения редки. Один транзакционный замок делает проверку
// доступа атомарной с его отзывом и с параллельным импортом промокодов.
func (r *Repository) LockAdmin(ctx context.Context) error {
	_, err := r.tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7170701)`)
	return adminError(err)
}

func (r *Repository) AdminAccess(ctx context.Context, telegramID int64) (promotionadminentity.Access, error) {
	rows, err := r.tx.Query(ctx, `SELECT partner_id FROM administration_grants WHERE telegram_user_id = $1 ORDER BY partner_id`, telegramID)
	if err != nil {
		return promotionadminentity.Access{}, adminError(err)
	}
	defer rows.Close()
	var access promotionadminentity.Access
	for rows.Next() {
		var partnerID *int64
		if err := rows.Scan(&partnerID); err != nil {
			return promotionadminentity.Access{}, adminError(err)
		}
		if partnerID == nil {
			access.Organizer = true
		} else {
			access.PartnerIDs = append(access.PartnerIDs, *partnerID)
		}
	}
	return access, adminError(rows.Err())
}

func (r *Repository) AdminCampaign(ctx context.Context) (*promotionadminentity.CampaignConfig, error) {
	var value promotionadminentity.CampaignConfig
	err := r.tx.QueryRow(ctx, `SELECT name, starts_on, ends_on FROM promotions WHERE id = 1`).Scan(&value.Name, &value.StartsOn, &value.EndsOn)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, adminError(err)
	}
	return &value, nil
}

func (r *Repository) updateCampaign(ctx context.Context, value promotionadminentity.CampaignConfig) error {
	// Финал удерживает ту же строку до фиксации снимка. Проверки должны видеть
	// его результат после ожидания, прежде чем разрешать изменение периода.
	if _, err := r.tx.Exec(ctx, `SELECT id FROM promotions WHERE id=1 FOR UPDATE`); err != nil {
		return adminError(err)
	}
	var invalid bool
	err := r.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_days WHERE date < $1::date OR date > $2::date)
		OR EXISTS (SELECT 1 FROM draws WHERE kind = 'final'
			AND scheduled_at < (($2::date + 1)::timestamp AT TIME ZONE 'Europe/Moscow'))`, value.StartsOn, value.EndsOn).Scan(&invalid)
	if err != nil {
		return adminError(err)
	}
	if invalid {
		return conflict("период исключает дни партнеров или выходит за расписание финала")
	}
	var frozen bool
	err = r.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draws JOIN promotions ON promotions.id = draws.promotion_id
		WHERE kind = 'final' AND performed_at IS NOT NULL AND (starts_on <> $1::date OR ends_on <> $2::date))`, value.StartsOn, value.EndsOn).Scan(&frozen)
	if err != nil {
		return adminError(err)
	}
	if frozen {
		return conflict("период акции после финального розыгрыша менять нельзя")
	}
	_, err = r.tx.Exec(ctx, `INSERT INTO promotions(name, starts_on, ends_on) VALUES ($1, $2, $3)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, starts_on = excluded.starts_on, ends_on = excluded.ends_on`, value.Name, value.StartsOn, value.EndsOn)
	return adminError(err)
}

func (r *Repository) AdminMaterials(ctx context.Context) (promotionadminentity.Materials, error) {
	var value promotionadminentity.Materials
	err := r.tx.QueryRow(ctx, `SELECT rules, participation_instructions, banner_ref, consent_text, consent_version
		FROM promotions WHERE id = 1`).Scan(&value.Rules, &value.Instructions, &value.BannerRef, &value.ConsentText, &value.ConsentVersion)
	return value, adminError(err)
}

func (r *Repository) updateMaterials(ctx context.Context, value promotionadminentity.Materials) error {
	var oldText, oldVersion string
	err := r.tx.QueryRow(ctx, `SELECT consent_text, consent_version FROM promotions WHERE id = 1 FOR UPDATE`).Scan(&oldText, &oldVersion)
	if err != nil {
		return adminError(err)
	}
	if oldText != "" && oldText != value.ConsentText && oldVersion == value.ConsentVersion {
		return conflict("при изменении согласия укажите новую версию")
	}
	if value.ConsentVersion != "" {
		var existing string
		err = r.tx.QueryRow(ctx, `SELECT consent_text FROM consent_versions WHERE version=$1`, value.ConsentVersion).Scan(&existing)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return adminError(err)
		}
		if err == nil && existing != value.ConsentText {
			return conflict("эта версия согласия уже содержит другой текст")
		}
		if _, err = r.tx.Exec(ctx, `INSERT INTO consent_versions(version,consent_text) VALUES($1,$2) ON CONFLICT DO NOTHING`, value.ConsentVersion, value.ConsentText); err != nil {
			return adminError(err)
		}
	}
	_, err = r.tx.Exec(ctx, `UPDATE promotions SET rules=$1, participation_instructions=$2, banner_ref=$3,
		consent_text=$4, consent_version=$5 WHERE id=1`, value.Rules, value.Instructions, value.BannerRef, value.ConsentText, value.ConsentVersion)
	return adminError(err)
}

func (r *Repository) AdminPolicies(ctx context.Context) (promotionadminentity.Policies, error) {
	var value promotionadminentity.Policies
	err := r.tx.QueryRow(ctx, `SELECT coalesce(winner_response_hours,0), coalesce(response_starts_on,''),
		coalesce(undeliverable_policy,'') FROM administration_settings WHERE id=1`).Scan(
		&value.WinnerResponseHours, &value.ResponseStartsOn, &value.UndeliverablePolicy)
	return value, adminError(err)
}

func (r *Repository) updatePolicies(ctx context.Context, value promotionadminentity.Policies) error {
	_, err := r.tx.Exec(ctx, `UPDATE administration_settings SET winner_response_hours=nullif($1,0),
		response_starts_on=nullif($2,''), undeliverable_policy=nullif($3,'') WHERE id=1`,
		value.WinnerResponseHours, value.ResponseStartsOn, value.UndeliverablePolicy)
	return adminError(err)
}

func (r *Repository) AdminPartners(ctx context.Context) ([]promotionadminentity.PartnerConfig, error) {
	rows, err := r.tx.Query(ctx, `SELECT id, name, telegram_chat_id, subscription_url FROM partners ORDER BY id`)
	if err != nil {
		return nil, adminError(err)
	}
	defer rows.Close()
	values := make([]promotionadminentity.PartnerConfig, 0)
	for rows.Next() {
		var value promotionadminentity.PartnerConfig
		if err := rows.Scan(&value.ID, &value.Name, &value.TelegramChatID, &value.SubscriptionURL); err != nil {
			return nil, adminError(err)
		}
		values = append(values, value)
	}
	return values, adminError(rows.Err())
}

func (r *Repository) updatePartner(ctx context.Context, value promotionadminentity.PartnerConfig) (int64, error) {
	var id int64
	var err error
	if value.ID == 0 {
		err = r.tx.QueryRow(ctx, `INSERT INTO partners(name,telegram_chat_id,subscription_url) VALUES($1,$2,$3) RETURNING id`,
			value.Name, value.TelegramChatID, value.SubscriptionURL).Scan(&id)
	} else {
		err = r.tx.QueryRow(ctx, `UPDATE partners SET name=$2,telegram_chat_id=$3,subscription_url=$4 WHERE id=$1 RETURNING id`,
			value.ID, value.Name, value.TelegramChatID, value.SubscriptionURL).Scan(&id)
	}
	return id, adminError(err)
}

func (r *Repository) AdminDays(ctx context.Context) ([]promotionadminentity.DayConfig, error) {
	rows, err := r.tx.Query(ctx, `SELECT id,partner_id,date,opens_at,closes_at,promo_instructions,promo_valid_until,is_enabled
		FROM partner_days ORDER BY date,id`)
	if err != nil {
		return nil, adminError(err)
	}
	defer rows.Close()
	values := make([]promotionadminentity.DayConfig, 0)
	for rows.Next() {
		var value promotionadminentity.DayConfig
		if err := rows.Scan(&value.ID, &value.PartnerID, &value.Date, &value.OpensAt, &value.ClosesAt, &value.PromoInstructions, &value.PromoValidUntil, &value.IsEnabled); err != nil {
			return nil, adminError(err)
		}
		values = append(values, value)
	}
	return values, adminError(rows.Err())
}

func (r *Repository) updateDay(ctx context.Context, value promotionadminentity.DayConfig) (int64, error) {
	var inPeriod bool
	err := r.tx.QueryRow(ctx, `SELECT $1::date BETWEEN starts_on AND ends_on FROM promotions WHERE id=1 FOR SHARE`, value.Date).Scan(&inPeriod)
	if err != nil {
		return 0, adminError(err)
	}
	if !inPeriod {
		return 0, conflict("день партнера должен входить в период акции")
	}
	if value.ID != 0 {
		var oldPartner int64
		var oldDate time.Time
		err = r.tx.QueryRow(ctx, `SELECT partner_id,date FROM partner_days WHERE id=$1 FOR UPDATE`, value.ID).Scan(&oldPartner, &oldDate)
		if err != nil {
			return 0, adminError(err)
		}
		var frozen bool
		err = r.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draws WHERE partner_day_id=$1 AND performed_at IS NOT NULL)`, value.ID).Scan(&frozen)
		if err != nil {
			return 0, adminError(err)
		}
		if frozen {
			return 0, conflict("день проведенного розыгрыша менять нельзя")
		}
		if oldPartner != value.PartnerID || oldDate.Format(time.DateOnly) != value.Date.Format(time.DateOnly) {
			var participated bool
			err = r.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM registrations WHERE partner_day_id=$1)
				OR EXISTS(SELECT 1 FROM promo_codes WHERE partner_day_id=$1)
				OR EXISTS(SELECT 1 FROM promo_code_requests WHERE partner_day_id=$1)`, value.ID).Scan(&participated)
			if err != nil {
				return 0, adminError(err)
			}
			if participated {
				return 0, conflict("партнера и дату дня с участием менять нельзя")
			}
		}
		var afterDraw bool
		err = r.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draws WHERE partner_day_id=$1 AND scheduled_at < $2)`, value.ID, value.ClosesAt).Scan(&afterDraw)
		if err != nil {
			return 0, adminError(err)
		}
		if afterDraw {
			return 0, conflict("прием должен закончиться до начала розыгрыша")
		}
	}
	var id int64
	if value.ID == 0 {
		err = r.tx.QueryRow(ctx, `INSERT INTO partner_days(partner_id,date,opens_at,closes_at,promo_instructions,promo_valid_until,is_enabled)
			VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, value.PartnerID, value.Date, value.OpensAt, value.ClosesAt, value.PromoInstructions, value.PromoValidUntil, value.IsEnabled).Scan(&id)
	} else {
		err = r.tx.QueryRow(ctx, `UPDATE partner_days SET partner_id=$2,date=$3,opens_at=$4,closes_at=$5,promo_instructions=$6,promo_valid_until=$7,is_enabled=$8
			WHERE id=$1 RETURNING id`, value.ID, value.PartnerID, value.Date, value.OpensAt, value.ClosesAt, value.PromoInstructions, value.PromoValidUntil, value.IsEnabled).Scan(&id)
	}
	return id, adminError(err)
}

func (r *Repository) AdminDraws(ctx context.Context) ([]promotionadminentity.DrawConfig, error) {
	rows, err := r.tx.Query(ctx, `SELECT draw.id,coalesce(draw.partner_day_id,0),draw.kind,draw.scheduled_at,
		prize.name,prize.instructions,prize.places_count,prize.units_per_winner
		FROM draws draw LEFT JOIN prizes prize ON prize.draw_id=draw.id ORDER BY draw.id,prize.id`)
	if err != nil {
		return nil, adminError(err)
	}
	defer rows.Close()
	values := make([]promotionadminentity.DrawConfig, 0)
	for rows.Next() {
		var value promotionadminentity.DrawConfig
		var name, instructions *string
		var places, units *int
		if err := rows.Scan(&value.ID, &value.PartnerDayID, &value.Kind, &value.ScheduledAt, &name, &instructions, &places, &units); err != nil {
			return nil, adminError(err)
		}
		if len(values) == 0 || values[len(values)-1].ID != value.ID {
			values = append(values, value)
		}
		if name != nil {
			values[len(values)-1].Prizes = append(values[len(values)-1].Prizes, promotionadminentity.PrizeConfig{Name: *name, Instructions: *instructions, PlacesCount: *places, UnitsPerWinner: *units})
		}
	}
	return values, adminError(rows.Err())
}

func (r *Repository) updateDraw(ctx context.Context, value promotionadminentity.DrawConfig) (int64, error) {
	// Общий порядок блокировок с приемом участников: сначала день, затем розыгрыш.
	var earliest time.Time
	if value.Kind == "daily" {
		var enabled bool
		err := r.tx.QueryRow(ctx, `SELECT closes_at,is_enabled FROM partner_days WHERE id=$1 FOR UPDATE`, value.PartnerDayID).Scan(&earliest, &enabled)
		if err != nil {
			return 0, adminError(err)
		}
		if !enabled {
			return 0, conflict("день партнера отключен")
		}
	} else {
		err := r.tx.QueryRow(ctx, `SELECT (ends_on+1)::timestamp AT TIME ZONE 'Europe/Moscow' FROM promotions WHERE id=1 FOR SHARE`).Scan(&earliest)
		if err != nil {
			return 0, adminError(err)
		}
	}
	if value.ID != 0 {
		var kind string
		var dayID int64
		var performed *time.Time
		err := r.tx.QueryRow(ctx, `SELECT kind,coalesce(partner_day_id,0),performed_at FROM draws WHERE id=$1 FOR UPDATE`, value.ID).Scan(&kind, &dayID, &performed)
		if err != nil {
			return 0, adminError(err)
		}
		if performed != nil {
			return 0, conflict("проведенный розыгрыш менять нельзя")
		}
		if kind != value.Kind || dayID != value.PartnerDayID {
			return 0, conflict("тип и день существующего розыгрыша менять нельзя")
		}
		var hasSnapshot bool
		err = r.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draw_participants WHERE draw_id=$1)`, value.ID).Scan(&hasSnapshot)
		if err != nil {
			return 0, adminError(err)
		}
		if hasSnapshot {
			return 0, conflict("розыгрыш с зафиксированными участниками менять нельзя")
		}
	}
	if value.ScheduledAt.Before(earliest) {
		return 0, conflict("розыгрыш должен проходить после окончания приема")
	}
	var id int64
	var err error
	if value.ID == 0 {
		err = r.tx.QueryRow(ctx, `INSERT INTO draws(kind,partner_day_id,scheduled_at) VALUES($1,nullif($2::bigint,0),$3) RETURNING id`, value.Kind, value.PartnerDayID, value.ScheduledAt).Scan(&id)
	} else {
		err = r.tx.QueryRow(ctx, `UPDATE draws SET scheduled_at=$2 WHERE id=$1 RETURNING id`, value.ID, value.ScheduledAt).Scan(&id)
	}
	if err != nil {
		return 0, adminError(err)
	}
	if _, err = r.tx.Exec(ctx, `DELETE FROM prizes WHERE draw_id=$1`, id); err != nil {
		return 0, adminError(err)
	}
	for _, prize := range value.Prizes {
		if _, err = r.tx.Exec(ctx, `INSERT INTO prizes(draw_id,name,instructions,places_count,units_per_winner) VALUES($1,$2,$3,$4,$5)`,
			id, prize.Name, prize.Instructions, prize.PlacesCount, prize.UnitsPerWinner); err != nil {
			return 0, adminError(err)
		}
	}
	return id, nil
}

func (r *Repository) AdminGrants(ctx context.Context) ([]promotionadminentity.Grant, error) {
	rows, err := r.tx.Query(ctx, `SELECT telegram_user_id,coalesce(partner_id,0) FROM administration_grants ORDER BY telegram_user_id,partner_id`)
	if err != nil {
		return nil, adminError(err)
	}
	defer rows.Close()
	values := make([]promotionadminentity.Grant, 0)
	for rows.Next() {
		var value promotionadminentity.Grant
		if err := rows.Scan(&value.TelegramID, &value.PartnerID); err != nil {
			return nil, adminError(err)
		}
		values = append(values, value)
	}
	return values, adminError(rows.Err())
}

func (r *Repository) updateGrant(ctx context.Context, telegramID, partnerID int64, enabled bool) error {
	var err error
	if enabled {
		_, err = r.tx.Exec(ctx, `INSERT INTO administration_grants(telegram_user_id,partner_id) VALUES($1,nullif($2::bigint,0)) ON CONFLICT DO NOTHING`, telegramID, partnerID)
	} else {
		_, err = r.tx.Exec(ctx, `DELETE FROM administration_grants WHERE telegram_user_id=$1 AND partner_id IS NOT DISTINCT FROM nullif($2::bigint,0)`, telegramID, partnerID)
	}
	if err != nil {
		return adminError(err)
	}
	return commands.New(r.tx).Enqueue(ctx, telegramID)
}

func conflict(message string) error {
	return fmt.Errorf("%w: %s", promotionadminentity.ErrConflict, message)
}

func adminError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return promotionadminentity.ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503", "23505", "23514":
			return promotionadminentity.ErrConflict
		}
	}
	return fmt.Errorf("administration repository: %w", err)
}
