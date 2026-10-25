package awards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
	telegramprotocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type AwardProcessor struct {
	pool   *pgxpool.Pool
	render func(awardsentity.Award) ([]json.RawMessage, error)
}

func NewAwardProcessor(pool *pgxpool.Pool, render func(awardsentity.Award) ([]json.RawMessage, error)) *AwardProcessor {
	return &AwardProcessor{pool: pool, render: render}
}

const claimConfiguration = `SELECT settings.winner_response_hours, settings.response_starts_on,
    settings.undeliverable_policy, consent.version, consent.consent_text
    FROM administration_settings settings CROSS JOIN promotions promotion
    JOIN consent_versions consent ON consent.version = promotion.consent_version
        AND consent.consent_text = promotion.consent_text
    WHERE settings.id = 1 AND promotion.id = 1 AND settings.winner_response_hours IS NOT NULL
        AND settings.response_starts_on IS NOT NULL AND settings.undeliverable_policy IS NOT NULL`

const claimNeedsProcessing = `award.is_current AND award.confirmed_at IS NULL AND (
    (award.response_hours IS NULL AND EXISTS (` + claimConfiguration + `))
    OR (award.response_hours IS NOT NULL AND award.claim_state IN ('pending', 'notified') AND (
        award.notice_queued_at IS NULL
        OR award.response_deadline <= clock_timestamp()
        OR (award.claim_state = 'pending' AND (
            EXISTS (SELECT 1 FROM outbox_messages message WHERE message.prize_award_id = award.id AND message.status = 'failed')
            OR (EXISTS (SELECT 1 FROM outbox_messages message WHERE message.prize_award_id = award.id)
                AND NOT EXISTS (SELECT 1 FROM outbox_messages message
                    WHERE message.prize_award_id = award.id AND message.status <> 'sent')))))))`

// Задание уведомления завершено, когда для каждого исходного победителя
// сохранена отправка либо назначение уже закрыто. Доставку отслеживают сами awards.
const initialNoticesReady = `NOT EXISTS (
    SELECT 1 FROM draw_results result
    LEFT JOIN prize_awards award ON award.draw_result_id = result.id
    WHERE result.draw_id = draw.id AND result.role = 'winner' AND (
        award.id IS NULL OR (award.notice_queued_at IS NULL AND award.confirmed_at IS NULL AND award.is_current)))`

const readyNotificationJob = `EXISTS (SELECT 1 FROM background_jobs job
    WHERE job.kind = 'notify_draw_winners' AND job.payload->>'draw_id' = draw.id::text
    AND job.status = 'pending' AND job.available_at <= clock_timestamp())`

// ProcessNext выполняет один переход получения приза вместе с его outbox.
// Вызовов Telegram внутри транзакции нет; один draw сериализует подтверждение и замену.
func (p *AwardProcessor) ProcessNext(ctx context.Context) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin prize processing: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var drawID int64
	err = tx.QueryRow(ctx, `SELECT draw.id FROM draws draw WHERE draw.approved_at IS NOT NULL AND (
		(EXISTS (`+claimConfiguration+`) AND EXISTS (
			SELECT 1 FROM draw_results result WHERE result.draw_id = draw.id AND result.role = 'winner'
			AND NOT EXISTS (SELECT 1 FROM prize_awards award WHERE award.draw_result_id = result.id)))
		OR EXISTS (SELECT 1 FROM prize_awards award WHERE award.draw_id = draw.id AND `+claimNeedsProcessing+`)
		OR (`+readyNotificationJob+` AND `+initialNoticesReady+`))
		ORDER BY draw.id LIMIT 1 FOR UPDATE OF draw SKIP LOCKED`).Scan(&drawID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find prize draw: %w", err)
	}
	if err = initializeAwards(ctx, tx, drawID); err != nil {
		return false, err
	}
	var awardID int64
	err = tx.QueryRow(ctx, `SELECT award.id FROM prize_awards award WHERE award.draw_id = $1
		AND `+claimNeedsProcessing+` ORDER BY award.id LIMIT 1 FOR UPDATE OF award`, drawID).Scan(&awardID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("find prize award: %w", err)
	}
	if err == nil {
		if err = p.processAward(ctx, tx, awardID); err != nil {
			return false, err
		}
	}
	if err = completeNotificationJob(ctx, tx, drawID); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit prize processing: %w", err)
	}
	return true, nil
}

func completeNotificationJob(ctx context.Context, tx pgx.Tx, drawID int64) error {
	_, err := tx.Exec(ctx, `UPDATE background_jobs job SET status = 'completed',
		completed_at = clock_timestamp(), attempts = attempts + 1
		FROM draws draw WHERE draw.id = $1 AND job.kind = 'notify_draw_winners'
		AND job.payload->>'draw_id' = draw.id::text AND job.status = 'pending'
		AND job.available_at <= clock_timestamp() AND `+initialNoticesReady, drawID)
	if err != nil {
		return fmt.Errorf("complete winner notification job: %w", err)
	}
	return nil
}

func initializeAwards(ctx context.Context, tx pgx.Tx, drawID int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO prize_awards(draw_id, draw_result_id, prize_id, place_number,
		response_hours, response_starts_on, undeliverable_policy, claim_consent_version, claim_consent_text,
		response_deadline)
		SELECT result.draw_id, result.id, result.prize_id, result.place_number, config.winner_response_hours,
		config.response_starts_on, config.undeliverable_policy, config.version, config.consent_text,
		CASE WHEN config.response_starts_on = 'approved' THEN draw.approved_at + config.winner_response_hours * interval '1 hour' END
		FROM draw_results result JOIN draws draw ON draw.id = result.draw_id
		CROSS JOIN (`+claimConfiguration+`) config WHERE result.draw_id = $1 AND result.role = 'winner'
		ON CONFLICT (draw_result_id) DO NOTHING`, drawID)
	if err != nil {
		return fmt.Errorf("initialize winner awards: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE prize_awards award SET response_hours = config.winner_response_hours,
		response_starts_on = config.response_starts_on, undeliverable_policy = config.undeliverable_policy,
		claim_consent_version = config.version, claim_consent_text = config.consent_text,
		response_deadline = CASE WHEN config.response_starts_on = 'approved'
			THEN (CASE WHEN award.previous_award_id IS NULL THEN draw.approved_at ELSE award.assigned_at END)
				+ config.winner_response_hours * interval '1 hour' END
		FROM draws draw, (`+claimConfiguration+`) config
		WHERE award.draw_id = draw.id AND draw.id = $1 AND award.response_hours IS NULL
		AND award.is_current AND award.confirmed_at IS NULL`, drawID)
	if err != nil {
		return fmt.Errorf("snapshot prize claim policy: %w", err)
	}
	return nil
}

func (p *AwardProcessor) processAward(ctx context.Context, tx pgx.Tx, awardID int64) error {
	award, err := NewAwards(tx).ViewAdmin(ctx, awardID)
	if err != nil {
		return err
	}
	var queued, failed, delivered bool
	var policy string
	err = tx.QueryRow(ctx, `SELECT award.notice_queued_at IS NOT NULL, award.undeliverable_policy,
		EXISTS (SELECT 1 FROM outbox_messages message WHERE message.prize_award_id = award.id AND message.status = 'failed'),
		EXISTS (SELECT 1 FROM outbox_messages message WHERE message.prize_award_id = award.id)
		AND NOT EXISTS (SELECT 1 FROM outbox_messages message WHERE message.prize_award_id = award.id AND message.status <> 'sent')
		FROM prize_awards award WHERE award.id = $1`, awardID).Scan(&queued, &policy, &failed, &delivered)
	if err != nil {
		return fmt.Errorf("read prize delivery: %w", err)
	}
	if failed {
		if policy == "reserve" {
			return replaceAward(ctx, tx, awardID)
		}
		_, err = tx.Exec(ctx, `UPDATE prize_awards SET claim_state = 'manual', response_deadline = NULL WHERE id = $1`, awardID)
		return err
	}
	if award.Deadline != nil && !award.Now.Before(*award.Deadline) {
		return replaceAward(ctx, tx, awardID)
	}
	if !queued {
		return p.enqueueNotice(ctx, tx, *award)
	}
	if delivered {
		_, err = tx.Exec(ctx, `UPDATE prize_awards SET claim_state = 'notified',
			notified_at = delivery.sent_at,
			response_deadline = CASE WHEN response_starts_on = 'delivered'
				THEN delivery.sent_at + response_hours * interval '1 hour' ELSE response_deadline END
			FROM (SELECT max(sent_at) AS sent_at FROM outbox_messages WHERE prize_award_id = $1) delivery
			WHERE id = $1`, awardID)
		return err
	}
	return nil
}

// Обновляет фактическую доставку и при пользовательском подтверждении, чтобы
// задержка фонового обработчика не сдвигала срок ответа.
func refreshAwardDelivery(ctx context.Context, tx pgx.Tx, awardID int64) error {
	_, err := tx.Exec(ctx, `UPDATE prize_awards award SET claim_state = 'manual', response_deadline = NULL
		WHERE award.id = $1 AND award.is_current AND award.confirmed_at IS NULL
		AND award.claim_state = 'pending' AND award.undeliverable_policy = 'manual'
		AND EXISTS (SELECT 1 FROM outbox_messages WHERE prize_award_id = award.id AND status = 'failed')`, awardID)
	if err != nil {
		return fmt.Errorf("freeze undelivered prize: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE prize_awards award SET claim_state = 'notified', notified_at = delivery.sent_at,
		response_deadline = CASE WHEN award.response_starts_on = 'delivered'
			THEN delivery.sent_at + award.response_hours * interval '1 hour' ELSE award.response_deadline END
		FROM (SELECT max(sent_at) sent_at FROM outbox_messages WHERE prize_award_id = $1
			HAVING count(*) > 0 AND bool_and(status = 'sent')) delivery
		WHERE award.id = $1 AND award.is_current AND award.confirmed_at IS NULL
		AND award.claim_state = 'pending' AND award.notice_queued_at IS NOT NULL`, awardID)
	if err != nil {
		return fmt.Errorf("refresh prize delivery: %w", err)
	}
	return nil
}

func (p *AwardProcessor) enqueueNotice(ctx context.Context, tx pgx.Tx, award awardsentity.Award) error {
	payloads, err := p.render(award)
	if err != nil {
		return fmt.Errorf("render prize notification: %w", err)
	}
	if len(payloads) == 0 {
		return errors.New("prize notification is empty")
	}
	for part, payload := range payloads {
		_, err = tx.Exec(ctx, `INSERT INTO outbox_messages(user_id, prize_award_id, kind, payload, deduplication_key)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (deduplication_key) DO NOTHING`, award.UserID, award.ID,
			telegramprotocol.MessageKindText, payload, fmt.Sprintf("award:%d:notice:%d", award.ID, part))
		if err != nil {
			return fmt.Errorf("enqueue prize notification: %w", err)
		}
	}
	_, err = tx.Exec(ctx, `UPDATE prize_awards SET notice_queued_at = clock_timestamp() WHERE id = $1`, award.ID)
	return err
}

func replaceAward(ctx context.Context, tx pgx.Tx, awardID int64) error {
	var reserveID int64
	err := tx.QueryRow(ctx, `SELECT result.id FROM draw_results result JOIN prize_awards previous
		ON previous.draw_id = result.draw_id WHERE previous.id = $1 AND result.role = 'reserve'
		AND NOT EXISTS (SELECT 1 FROM prize_awards used WHERE used.draw_result_id = result.id)
		ORDER BY result.position LIMIT 1`, awardID).Scan(&reserveID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find reserve recipient: %w", err)
	}
	state := awardsentity.AwardReplaced
	if errors.Is(err, pgx.ErrNoRows) {
		state = awardsentity.AwardExhausted
	}
	_, err = tx.Exec(ctx, `UPDATE prize_awards SET is_current = false, claim_state = $2 WHERE id = $1`, awardID, state)
	if err != nil {
		return fmt.Errorf("finish previous prize assignment: %w", err)
	}
	// Инвалидация lease запрещает sender подтвердить старую отправку после замены.
	_, err = tx.Exec(ctx, `UPDATE outbox_messages SET status = 'failed', lease_until = NULL, attempt_token = NULL
		WHERE prize_award_id = $1 AND status IN ('pending', 'processing')`, awardID)
	if err != nil {
		return fmt.Errorf("cancel obsolete prize notification: %w", err)
	}
	if state == awardsentity.AwardExhausted {
		return nil
	}
	_, err = tx.Exec(ctx, `WITH moment AS MATERIALIZED (SELECT clock_timestamp() AS now)
		INSERT INTO prize_awards(draw_id, draw_result_id, prize_id, place_number, previous_award_id, assigned_at,
		response_hours, response_starts_on, undeliverable_policy, claim_consent_version, claim_consent_text, response_deadline)
		SELECT draw_id, $2, prize_id, place_number, id, moment.now, response_hours, response_starts_on,
		undeliverable_policy, claim_consent_version, claim_consent_text,
		CASE WHEN response_starts_on = 'approved' THEN moment.now + response_hours * interval '1 hour' END
		FROM prize_awards CROSS JOIN moment WHERE id = $1`, awardID, reserveID)
	if err != nil {
		return fmt.Errorf("assign prize to reserve recipient: %w", err)
	}
	return nil
}
