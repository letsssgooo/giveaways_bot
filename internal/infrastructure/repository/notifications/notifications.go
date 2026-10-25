// Package notifications содержит доступ к рассылкам и очереди уведомлений в PostgreSQL.
package notifications

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/notifications/broadcasts"

	telegramprotocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

const (
	statusPending       = "pending"
	statusProcessing    = "processing"
	statusSent          = "sent"
	statusFailed        = "failed"
	perChatSendInterval = time.Second
)

const (
	uuidSize               = 16
	uuidVersionByte        = 6
	uuidVariantByte        = 8
	uuidVersionClearMask   = 0x0f
	uuidVersion4Bits       = 0x40
	uuidVariantClearMask   = 0x3f
	uuidRFC4122VariantBits = 0x80
)

var ErrLeaseLost = errors.New("outbox lease is no longer owned")

type Repository struct {
	*broadcasts.Repository
	tx pgx.Tx
}

func New(tx pgx.Tx) *Repository {
	return &Repository{Repository: broadcasts.New(tx), tx: tx}
}

// Enqueue сохраняет отправку в транзакции, изменяющей состояние сценария.
func (r *Repository) Enqueue(ctx context.Context, userID, updateID int64, kind string, payload json.RawMessage, key string) error {
	_, err := r.tx.Exec(ctx, `INSERT INTO outbox_messages
		(user_id, update_id, kind, payload, deduplication_key) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (deduplication_key) DO NOTHING`, userID, updateID, kind, payload, key)
	if err != nil {
		return fmt.Errorf("enqueue Telegram message: %w", err)
	}
	return nil
}

type Message struct {
	ID           int64
	UserID       int64
	ChatID       int64
	Kind         string
	Payload      json.RawMessage
	Attempts     int
	AttemptToken string
}

type Queue struct {
	pool *pgxpool.Pool
}

func NewQueue(pool *pgxpool.Pool) *Queue {
	return &Queue{pool: pool}
}

// Claim резервирует одну отправку. Истекшая аренда позволяет продолжить после сбоя.
// Сообщения сохраняют порядок и интервал в секунду. Ответы на нажатия кнопок
// получают приоритет и не ждут предыдущих сообщений или их повторной доставки.
// Общая пауза после Telegram 429 действует и на ответы кнопок, включая рестарт.
func (q *Queue) Claim(ctx context.Context, leaseDuration time.Duration) (*Message, error) {
	if leaseDuration < time.Microsecond {
		return nil, errors.New("outbox lease must be at least one microsecond")
	}
	token, err := attemptToken()
	if err != nil {
		return nil, err
	}
	var message Message
	err = q.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT message.id FROM outbox_messages AS message
		WHERE EXISTS (SELECT 1 FROM telegram_delivery_state
			WHERE id AND blocked_until <= clock_timestamp())
		AND ((message.status = '`+statusPending+`' AND message.available_at <= clock_timestamp())
			OR (message.status = '`+statusProcessing+`' AND message.lease_until <= clock_timestamp()))
		AND (message.kind = '`+telegramprotocol.MessageKindCallbackAnswer+`' OR (
			NOT EXISTS (
				SELECT 1 FROM outbox_messages AS earlier
				WHERE earlier.user_id = message.user_id AND earlier.id < message.id
				AND earlier.kind <> '`+telegramprotocol.MessageKindCallbackAnswer+`'
				AND earlier.status IN ('`+statusPending+`', '`+statusProcessing+`'))
			AND NOT EXISTS (
				SELECT 1 FROM outbox_messages AS recent
				WHERE recent.user_id = message.user_id AND recent.status = '`+statusSent+`'
				AND recent.kind <> '`+telegramprotocol.MessageKindCallbackAnswer+`'
				AND recent.sent_at > clock_timestamp() - $3 * interval '1 microsecond')))
		ORDER BY (message.kind = '`+telegramprotocol.MessageKindCallbackAnswer+`') DESC, message.id
		LIMIT 1 FOR UPDATE OF message SKIP LOCKED
	), claimed AS (
		UPDATE outbox_messages AS message SET status = '`+statusProcessing+`',
			lease_until = clock_timestamp() + $1 * interval '1 microsecond',
			attempt_token = $2::uuid, attempts = attempts + 1
		FROM candidate WHERE message.id = candidate.id
		RETURNING message.id, message.user_id, message.kind, message.payload,
			message.attempts, message.attempt_token
	)
	SELECT claimed.id, claimed.user_id, users.chat_id, claimed.kind, claimed.payload,
		claimed.attempts, claimed.attempt_token::text
	FROM claimed JOIN users ON users.id = claimed.user_id`, leaseDuration.Microseconds(), token, perChatSendInterval.Microseconds()).
		Scan(&message.ID, &message.UserID, &message.ChatID, &message.Kind, &message.Payload,
			&message.Attempts, &message.AttemptToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim Telegram message: %w", err)
	}
	return &message, nil
}

func (q *Queue) Sent(ctx context.Context, id int64, token string) error {
	result, err := q.pool.Exec(ctx, `UPDATE outbox_messages SET status = '`+statusSent+`',
		sent_at = clock_timestamp(), lease_until = NULL, attempt_token = NULL
		WHERE id = $1 AND status = '`+statusProcessing+`' AND attempt_token = $2::uuid`, id, token)
	if err != nil {
		return fmt.Errorf("mark Telegram message sent: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Retry откладывает отправку либо завершает ее после постоянной ошибки Telegram.
func (q *Queue) Retry(ctx context.Context, id int64, token string, delay time.Duration, permanent bool) error {
	if delay < 0 {
		return errors.New("outbox retry delay must not be negative")
	}
	status := statusPending
	if permanent {
		status = statusFailed
	}
	result, err := q.pool.Exec(ctx, `UPDATE outbox_messages SET status = $3,
		available_at = clock_timestamp() + $4 * interval '1 microsecond',
		lease_until = NULL, attempt_token = NULL
		WHERE id = $1 AND status = '`+statusProcessing+`' AND attempt_token = $2::uuid`,
		id, token, status, delay.Microseconds())
	if err != nil {
		return fmt.Errorf("retry Telegram message: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// RateLimited атомарно откладывает сообщение и все отправки бота после Telegram
// 429. Пауза сохраняется при перезапуске; устаревшая попытка не меняет очередь.
func (q *Queue) RateLimited(ctx context.Context, id int64, token string, delay time.Duration) error {
	if delay <= 0 {
		return errors.New("telegram rate-limit delay must be positive")
	}
	result, err := q.pool.Exec(ctx, `WITH retried AS (
		UPDATE outbox_messages SET status = '`+statusPending+`',
			available_at = clock_timestamp() + $3 * interval '1 microsecond',
			lease_until = NULL, attempt_token = NULL
		WHERE id = $1 AND status = '`+statusProcessing+`' AND attempt_token = $2::uuid
		RETURNING available_at
	)
	UPDATE telegram_delivery_state SET blocked_until = greatest(blocked_until, retried.available_at)
	FROM retried WHERE telegram_delivery_state.id`, id, token, delay.Microseconds())
	if err != nil {
		return fmt.Errorf("persist Telegram rate-limit pause: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

func attemptToken() (string, error) {
	var token [uuidSize]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("create outbox attempt token: %w", err)
	}
	token[uuidVersionByte] = (token[uuidVersionByte] & uuidVersionClearMask) | uuidVersion4Bits
	token[uuidVariantByte] = (token[uuidVariantByte] & uuidVariantClearMask) | uuidRFC4122VariantBits
	return fmt.Sprintf("%x-%x-%x-%x-%x", token[:4], token[4:6], token[6:8], token[8:10], token[10:]), nil
}
