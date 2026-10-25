// Package participation содержит доступ к промокодам, заявкам и активным дням в PostgreSQL.
package registration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	registrationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation/registration"
)

type Queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type RegistrationRepository struct {
	query Queryer
}

// NewRegistration принимает пул для чтения перед проверкой Telegram, транзакцию — для записи.
func NewRegistration(query Queryer) *RegistrationRepository {
	return &RegistrationRepository{query: query}
}

func (r *RegistrationRepository) View(ctx context.Context, telegramID, dayID int64) (*registrationentity.RegistrationDay, error) {
	var day registrationentity.RegistrationDay
	err := r.query.QueryRow(ctx, `WITH moment AS MATERIALIZED (SELECT clock_timestamp() AS now)
		SELECT day.id, partner.name, partner.telegram_chat_id, partner.subscription_url,
			day.date, day.closes_at, moment.now,
			day.is_enabled AND day.date = (moment.now AT TIME ZONE 'Europe/Moscow')::date
			AND day.date BETWEEN promotion.starts_on AND promotion.ends_on
			AND day.opens_at <= moment.now AND moment.now < day.closes_at,
			registration.id IS NOT NULL,
			(SELECT count(*) FROM active_days WHERE user_id = participant.id)
		FROM partner_days AS day
		JOIN partners AS partner ON partner.id = day.partner_id
		JOIN promotions AS promotion ON promotion.id = day.promotion_id
		CROSS JOIN moment
		LEFT JOIN users AS participant ON participant.telegram_user_id = $1
		LEFT JOIN registrations AS registration ON registration.user_id = participant.id
			AND registration.partner_day_id = day.id
		WHERE day.id = $2`, telegramID, dayID).Scan(&day.ID, &day.Name, &day.TelegramChatID,
		&day.SubscriptionURL, &day.Date, &day.ClosesAt, &day.Now, &day.Open, &day.Registered, &day.Chances)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read registration day: %w", err)
	}
	return &day, nil
}

func (r *RegistrationRepository) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := r.query.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

// Lock и Save требуют явной транзакции: блокировки должны сохраняться до outbox.
func (r *RegistrationRepository) Lock(ctx context.Context, userID, telegramID, dayID int64) (*registrationentity.RegistrationDay, error) {
	if _, ok := r.query.(pgx.Tx); !ok {
		return nil, errors.New("registration requires a transaction")
	}
	var lockedID int64
	if err := r.query.QueryRow(ctx, `SELECT id FROM promotions WHERE id = 1 FOR SHARE`).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lock promotion: %w", err)
	}
	if err := r.query.QueryRow(ctx, `SELECT day.id FROM partner_days AS day
		JOIN partners AS partner ON partner.id = day.partner_id
		WHERE day.id = $1 FOR SHARE OF day, partner`, dayID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lock partner day: %w", err)
	}
	// ACK в outbox уже удерживает KEY SHARE через внешний ключ users.
	// NO KEY UPDATE сериализует действия пользователя и совместим с этим ключом.
	if err := r.query.QueryRow(ctx, `SELECT id FROM users
		WHERE id = $1 AND telegram_user_id = $2 FOR NO KEY UPDATE`, userID, telegramID).Scan(&lockedID); err != nil {
		return nil, fmt.Errorf("lock participant: %w", err)
	}
	return r.View(ctx, telegramID, dayID)
}

func (r *RegistrationRepository) Save(ctx context.Context, userID, dayID int64, registeredAt, checkedAt time.Time) (bool, int, error) {
	if _, ok := r.query.(pgx.Tx); !ok {
		return false, 0, errors.New("registration requires a transaction")
	}
	inserted, err := r.query.Exec(ctx, `INSERT INTO registrations
		(user_id, partner_day_id, registered_at, subscription_checked_at)
		VALUES ($1, $2, $3, $4) ON CONFLICT (user_id, partner_day_id) DO NOTHING`,
		userID, dayID, registeredAt, checkedAt)
	if err != nil {
		return false, 0, fmt.Errorf("insert registration: %w", err)
	}
	if inserted.RowsAffected() == 1 {
		if _, err := r.query.Exec(ctx, `INSERT INTO active_days(user_id, date, activated_at)
			VALUES ($1, ($2::timestamptz AT TIME ZONE 'Europe/Moscow')::date, $2)
			ON CONFLICT (user_id, date) DO NOTHING`, userID, registeredAt); err != nil {
			return false, 0, fmt.Errorf("activate participation day: %w", err)
		}
	}
	var chances int
	if err := r.query.QueryRow(ctx, `SELECT count(*) FROM active_days WHERE user_id = $1`, userID).Scan(&chances); err != nil {
		return false, 0, fmt.Errorf("count active days: %w", err)
	}
	return inserted.RowsAffected() == 1, chances, nil
}
