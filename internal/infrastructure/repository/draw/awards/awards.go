package awards

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
)

type Awards struct{ tx pgx.Tx }

func NewAwards(tx pgx.Tx) *Awards { return &Awards{tx: tx} }

const awardSelect = `SELECT award.id, award.draw_id, participant.user_id, users.telegram_user_id,
    COALESCE(day.partner_id, 0), draw.kind, prize.name, COALESCE(partner.name, ''),
    prize.instructions, prize.units_per_winner, award.is_current, award.claim_state,
    clock_timestamp(), award.response_deadline, award.notified_at, award.confirmed_at,
    award.consent_given_at, COALESCE(award.claim_consent_version, ''),
    COALESCE(award.claim_consent_text, ''), COALESCE(award.first_name, ''), COALESCE(award.last_name, ''),
    COALESCE(award.response_hours, 0), COALESCE(award.response_starts_on, ''), award.place_number
    FROM prize_awards award
    JOIN draws draw ON draw.id = award.draw_id
    JOIN draw_results result ON result.id = award.draw_result_id
    JOIN draw_participants participant ON participant.id = result.draw_participant_id
    JOIN users ON users.id = participant.user_id
    JOIN prizes prize ON prize.id = award.prize_id
    LEFT JOIN partner_days day ON day.id = draw.partner_day_id
    LEFT JOIN partners partner ON partner.id = day.partner_id
    WHERE draw.approved_at IS NOT NULL`

func scanAward(row pgx.Row) (*awardsentity.Award, error) {
	var award awardsentity.Award
	err := row.Scan(&award.ID, &award.DrawID, &award.UserID, &award.TelegramID,
		&award.PartnerID, &award.Kind, &award.PrizeName, &award.PartnerName,
		&award.Instructions, &award.Units, &award.Current, &award.State, &award.Now,
		&award.Deadline, &award.NotifiedAt, &award.ConfirmedAt, &award.ConsentGivenAt,
		&award.ConsentVersion, &award.ConsentText, &award.FirstName, &award.LastName,
		&award.ResponseHours, &award.ResponseStartsOn, &award.PlaceNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read prize award: %w", err)
	}
	return &award, nil
}

func (r *Awards) View(ctx context.Context, awardID, userID int64) (*awardsentity.Award, error) {
	return scanAward(r.tx.QueryRow(ctx, awardSelect+` AND award.id = $1 AND participant.user_id = $2`, awardID, userID))
}

// ViewAdmin не проверяет права: вызывающая сторона должна проверить доступ к розыгрышу.
func (r *Awards) ViewAdmin(ctx context.Context, awardID int64) (*awardsentity.Award, error) {
	return scanAward(r.tx.QueryRow(ctx, awardSelect+` AND award.id = $1`, awardID))
}

func (r *Awards) List(ctx context.Context, userID int64) ([]awardsentity.Award, error) {
	rows, err := r.tx.Query(ctx, awardSelect+` AND participant.user_id = $1 ORDER BY award.id DESC`, userID)
	return collectAwards(rows, err)
}

// ListIssues возвращает случаи ручного разбора; доступ к каждой записи проверяет сервис.
func (r *Awards) ListIssues(ctx context.Context) ([]awardsentity.Award, error) {
	rows, err := r.tx.Query(ctx, awardSelect+` AND award.claim_state IN ('manual', 'exhausted') ORDER BY award.id`)
	return collectAwards(rows, err)
}

func collectAwards(rows pgx.Rows, err error) ([]awardsentity.Award, error) {
	if err != nil {
		return nil, fmt.Errorf("list prize awards: %w", err)
	}
	defer rows.Close()
	var result []awardsentity.Award
	for rows.Next() {
		award, scanErr := scanAward(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, *award)
	}
	return result, rows.Err()
}

func (r *Awards) lock(ctx context.Context, awardID int64) (*awardsentity.Award, error) {
	var id int64
	err := r.tx.QueryRow(ctx, `SELECT draw.id FROM draws draw JOIN prize_awards award
		ON award.draw_id = draw.id WHERE award.id = $1 AND draw.approved_at IS NOT NULL
		FOR UPDATE OF draw`, awardID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, awardsentity.ErrAwardUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("lock prize draw: %w", err)
	}
	if err = r.tx.QueryRow(ctx, `SELECT id FROM prize_awards WHERE id = $1 FOR UPDATE`, awardID).Scan(&id); err != nil {
		return nil, fmt.Errorf("lock prize award: %w", err)
	}
	if err = refreshAwardDelivery(ctx, r.tx, awardID); err != nil {
		return nil, err
	}
	var deliveryFailed bool
	err = r.tx.QueryRow(ctx, `SELECT award.is_current AND award.confirmed_at IS NULL
		AND award.undeliverable_policy = 'reserve' AND EXISTS (
			SELECT 1 FROM outbox_messages WHERE prize_award_id = award.id AND status = 'failed')
		FROM prize_awards award WHERE id = $1`, awardID).Scan(&deliveryFailed)
	if err != nil {
		return nil, fmt.Errorf("read failed prize delivery: %w", err)
	}
	if deliveryFailed {
		return nil, awardsentity.ErrAwardUnavailable
	}
	return r.ViewAdmin(ctx, awardID)
}

func claimable(award *awardsentity.Award, userID int64) error {
	if award == nil || award.UserID != userID {
		return awardsentity.ErrAwardUnavailable
	}
	if award.ConfirmedAt != nil {
		return nil
	}
	if !award.Current || award.ConsentVersion == "" {
		return awardsentity.ErrAwardUnavailable
	}
	if award.Deadline != nil && !award.Now.Before(*award.Deadline) {
		return awardsentity.ErrClaimExpired
	}
	return nil
}

func (r *Awards) AcceptConsent(ctx context.Context, awardID, userID int64) (*awardsentity.Award, error) {
	award, err := r.lock(ctx, awardID)
	if err != nil {
		return nil, err
	}
	if err = claimable(award, userID); err != nil {
		return nil, err
	}
	if award.ConsentGivenAt != nil || award.ConfirmedAt != nil {
		return award, nil
	}
	_, err = r.tx.Exec(ctx, `UPDATE prize_awards SET consent_given_at = $2,
		consent_version = claim_consent_version WHERE id = $1`, awardID, award.Now)
	if err != nil {
		return nil, fmt.Errorf("accept prize consent: %w", err)
	}
	return r.View(ctx, awardID, userID)
}

func (r *Awards) Confirm(ctx context.Context, awardID, userID int64, firstName, lastName string) (*awardsentity.Award, error) {
	award, err := r.lock(ctx, awardID)
	if err != nil {
		return nil, err
	}
	if err = claimable(award, userID); err != nil {
		return nil, err
	}
	if award.ConfirmedAt != nil {
		return award, nil
	}
	if award.ConsentGivenAt == nil {
		return nil, awardsentity.ErrConsentRequired
	}
	firstName, lastName = strings.TrimSpace(firstName), strings.TrimSpace(lastName)
	if firstName == "" || lastName == "" {
		return nil, errors.New("имя и фамилия обязательны")
	}
	_, err = r.tx.Exec(ctx, `UPDATE prize_awards SET first_name = $2, last_name = $3,
		confirmed_at = $4, claim_state = 'confirmed' WHERE id = $1`, awardID, firstName, lastName, award.Now)
	if err != nil {
		return nil, fmt.Errorf("confirm prize: %w", err)
	}
	return r.View(ctx, awardID, userID)
}

// Retry повторяет только окончательно не доставленные части уведомления.
// Права администратора проверяются вызывающей стороной в той же транзакции.
func (r *Awards) Retry(ctx context.Context, awardID int64) error {
	award, err := r.lock(ctx, awardID)
	if err != nil {
		return err
	}
	if !award.Current || award.ConfirmedAt != nil || award.State != awardsentity.AwardManual {
		return awardsentity.ErrAwardUnavailable
	}
	_, err = r.tx.Exec(ctx, `UPDATE outbox_messages SET status = 'pending', available_at = clock_timestamp(),
		lease_until = NULL, attempt_token = NULL WHERE prize_award_id = $1 AND status = 'failed'`, awardID)
	if err != nil {
		return fmt.Errorf("retry prize notification: %w", err)
	}
	_, err = r.tx.Exec(ctx, `UPDATE prize_awards SET claim_state = 'pending',
		response_deadline = CASE WHEN response_starts_on = 'approved'
		THEN clock_timestamp() + response_hours * interval '1 hour' END WHERE id = $1`, awardID)
	return err
}

// Replace передает резервисту только приз с ручным решением о недоставке.
func (r *Awards) Replace(ctx context.Context, awardID int64) error {
	award, err := r.lock(ctx, awardID)
	if err != nil {
		return err
	}
	if !award.Current || award.ConfirmedAt != nil || award.State != awardsentity.AwardManual {
		return awardsentity.ErrAwardUnavailable
	}
	return replaceAward(ctx, r.tx, awardID)
}
