// Package participation содержит доступ к промокодам, заявкам и активным дням в PostgreSQL.
package participation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/participation"
)

type Repository struct {
	tx pgx.Tx
}

// New использует транзакцию обработки update: код, активность и ответ в outbox
// фиксируются вместе. Репозиторий самостоятельно не завершает транзакцию.
func New(tx pgx.Tx) *Repository {
	return &Repository{tx: tx}
}

func (r *Repository) CodeOptions(ctx context.Context, userID int64) ([]entity.CodeOption, error) {
	rows, err := r.tx.Query(ctx, `WITH moment AS MATERIALIZED (SELECT clock_timestamp() AS now)
		SELECT day.id, partner.name, day.date, code.id IS NOT NULL
		FROM partner_days AS day
		JOIN partners AS partner ON partner.id = day.partner_id
		JOIN promotions AS promotion ON promotion.id = day.promotion_id
		LEFT JOIN promo_codes AS code ON code.partner_day_id = day.id AND code.user_id = $1
		CROSS JOIN moment
		WHERE code.id IS NOT NULL OR (
			day.is_enabled AND day.date = (moment.now AT TIME ZONE 'Europe/Moscow')::date
			AND day.date BETWEEN promotion.starts_on AND promotion.ends_on
			AND day.opens_at <= moment.now AND moment.now < day.closes_at)
		ORDER BY day.date DESC, partner.name, day.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("read promo code options: %w", err)
	}
	defer rows.Close()
	options := make([]entity.CodeOption, 0)
	for rows.Next() {
		var option entity.CodeOption
		if err := rows.Scan(&option.PartnerDayID, &option.PartnerName, &option.Date, &option.HasCode); err != nil {
			return nil, fmt.Errorf("scan promo code option: %w", err)
		}
		options = append(options, option)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read promo code options: %w", err)
	}
	return options, nil
}

type partnerDay struct {
	result    entity.CodeResult
	partnerID int64
	opensAt   time.Time
	closesAt  time.Time
	isEnabled bool
}

// RequestCode сохраняет один результат на update_id. Порядок блокировок:
// update → акция → день партнера → пользователь → свободный код.
// Блокировка пользователя также объединяет активность разных партнеров за день.
func (r *Repository) RequestCode(ctx context.Context, userID, partnerDayID, updateID int64) (entity.CodeResult, error) {
	var lockedID int64
	if err := r.tx.QueryRow(ctx, `SELECT update_id FROM telegram_updates
		WHERE update_id = $1 FOR UPDATE`, updateID).Scan(&lockedID); err != nil {
		return entity.CodeResult{}, fmt.Errorf("lock promo code update: %w", err)
	}
	if result, found, err := r.recordedResult(ctx, userID, partnerDayID, updateID); found || err != nil {
		return result, err
	}

	var startsOn, endsOn time.Time
	err := r.tx.QueryRow(ctx, `SELECT starts_on, ends_on FROM promotions WHERE id = 1 FOR SHARE`).
		Scan(&startsOn, &endsOn)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.CodeResult{Outcome: entity.CodeUnavailable}, nil
	}
	if err != nil {
		return entity.CodeResult{}, fmt.Errorf("lock promotion for promo code: %w", err)
	}
	day, err := r.readDay(ctx, partnerDayID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.CodeResult{Outcome: entity.CodeUnavailable}, nil
	}
	if err != nil {
		return entity.CodeResult{}, err
	}
	if err := r.tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID).Scan(&lockedID); err != nil {
		return entity.CodeResult{}, fmt.Errorf("lock promo code user: %w", err)
	}

	result := day.result
	err = r.tx.QueryRow(ctx, `SELECT code FROM promo_codes WHERE user_id = $1 AND partner_day_id = $2`,
		userID, partnerDayID).Scan(&result.Code)
	if err == nil {
		result.Outcome = entity.CodeRepeated
		return r.finishRequest(ctx, userID, updateID, result)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return entity.CodeResult{}, fmt.Errorf("read previously issued code: %w", err)
	}
	return r.issueCode(ctx, userID, updateID, day, startsOn, endsOn)
}

func (r *Repository) issueCode(ctx context.Context, userID, updateID int64, day partnerDay, startsOn, endsOn time.Time) (entity.CodeResult, error) {
	result := day.result
	_, open, err := r.issuanceMoment(ctx, day, startsOn, endsOn)
	if err != nil {
		return entity.CodeResult{}, err
	}
	if !open {
		result.Outcome = entity.CodeClosed
		return r.finishRequest(ctx, userID, updateID, result)
	}

	var codeID int64
	err = r.tx.QueryRow(ctx, `SELECT id, code FROM promo_codes
		WHERE partner_id = $1 AND user_id IS NULL ORDER BY id LIMIT 1 FOR UPDATE`, day.partnerID).
		Scan(&codeID, &result.Code)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return entity.CodeResult{}, fmt.Errorf("lock available promo code: %w", err)
	}
	empty := errors.Is(err, pgx.ErrNoRows)
	issuedAt, open, err := r.issuanceMoment(ctx, day, startsOn, endsOn)
	if err != nil {
		return entity.CodeResult{}, err
	}
	if !open {
		result.Code = ""
		result.Outcome = entity.CodeClosed
		return r.finishRequest(ctx, userID, updateID, result)
	}
	if empty {
		result.Outcome = entity.CodeExhausted
		return r.finishRequest(ctx, userID, updateID, result)
	}
	if _, err := r.tx.Exec(ctx, `UPDATE promo_codes SET user_id = $1, partner_day_id = $2, issued_at = $3
		WHERE id = $4`, userID, result.PartnerDayID, issuedAt, codeID); err != nil {
		return entity.CodeResult{}, fmt.Errorf("assign promo code: %w", err)
	}
	if _, err := r.tx.Exec(ctx, `INSERT INTO active_days(user_id, date, activated_at) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, date) DO NOTHING`, userID, result.Date, issuedAt); err != nil {
		return entity.CodeResult{}, fmt.Errorf("record promo code active day: %w", err)
	}
	result.Outcome = entity.CodeIssued
	return r.finishRequest(ctx, userID, updateID, result)
}

func (r *Repository) readDay(ctx context.Context, id int64, lock bool) (partnerDay, error) {
	query := `SELECT day.id, day.partner_id, partner.name, day.date, day.opens_at, day.closes_at,
		day.promo_instructions, day.promo_valid_until, day.is_enabled
		FROM partner_days AS day JOIN partners AS partner ON partner.id = day.partner_id WHERE day.id = $1`
	if lock {
		query += ` FOR SHARE OF day`
	}
	var day partnerDay
	err := r.tx.QueryRow(ctx, query, id).Scan(&day.result.PartnerDayID, &day.partnerID, &day.result.PartnerName,
		&day.result.Date, &day.opensAt, &day.closesAt, &day.result.Instructions, &day.result.ValidUntil, &day.isEnabled)
	if err != nil {
		return partnerDay{}, fmt.Errorf("read promo code partner day: %w", err)
	}
	return day, nil
}

func (r *Repository) issuanceMoment(ctx context.Context, day partnerDay, startsOn, endsOn time.Time) (time.Time, bool, error) {
	var now, today time.Time
	if err := r.tx.QueryRow(ctx, `WITH moment AS MATERIALIZED (SELECT clock_timestamp() AS now)
		SELECT now, (now AT TIME ZONE 'Europe/Moscow')::date FROM moment`).Scan(&now, &today); err != nil {
		return time.Time{}, false, fmt.Errorf("read promo code processing time: %w", err)
	}
	open := day.isEnabled && day.result.Date.Equal(today) && !today.Before(startsOn) && !today.After(endsOn) &&
		!now.Before(day.opensAt) && now.Before(day.closesAt)
	return now, open, nil
}

func (r *Repository) recordedResult(ctx context.Context, userID, partnerDayID, updateID int64) (entity.CodeResult, bool, error) {
	var savedUserID, savedDayID int64
	var outcome entity.CodeOutcome
	err := r.tx.QueryRow(ctx, `SELECT user_id, partner_day_id, outcome FROM promo_code_requests WHERE update_id = $1`,
		updateID).Scan(&savedUserID, &savedDayID, &outcome)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.CodeResult{}, false, nil
	}
	if err != nil {
		return entity.CodeResult{}, false, fmt.Errorf("read recorded promo code request: %w", err)
	}
	if savedUserID != userID || savedDayID != partnerDayID {
		return entity.CodeResult{}, false, errors.New("promo code update belongs to another request")
	}
	day, err := r.readDay(ctx, partnerDayID, false)
	if err != nil {
		return entity.CodeResult{}, false, err
	}
	result := day.result
	result.Outcome = outcome
	if outcome == entity.CodeIssued || outcome == entity.CodeRepeated {
		if err := r.tx.QueryRow(ctx, `SELECT code FROM promo_codes WHERE user_id = $1 AND partner_day_id = $2`,
			userID, partnerDayID).Scan(&result.Code); err != nil {
			return entity.CodeResult{}, false, fmt.Errorf("read recorded promo code: %w", err)
		}
	}
	result.Chances, err = r.chances(ctx, userID)
	return result, true, err
}

func (r *Repository) finishRequest(ctx context.Context, userID, updateID int64, result entity.CodeResult) (entity.CodeResult, error) {
	if _, err := r.tx.Exec(ctx, `INSERT INTO promo_code_requests(user_id, partner_day_id, update_id, outcome)
		VALUES ($1, $2, $3, $4)`, userID, result.PartnerDayID, updateID, result.Outcome); err != nil {
		return entity.CodeResult{}, fmt.Errorf("record promo code request: %w", err)
	}
	var err error
	result.Chances, err = r.chances(ctx, userID)
	return result, err
}

func (r *Repository) chances(ctx context.Context, userID int64) (int, error) {
	var days int
	if err := r.tx.QueryRow(ctx, `SELECT count(*) FROM active_days WHERE user_id = $1`, userID).Scan(&days); err != nil {
		return 0, fmt.Errorf("count promo code active days: %w", err)
	}
	return min(days, entity.MaxFinalChances), nil
}
