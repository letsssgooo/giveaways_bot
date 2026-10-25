package broadcasts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/notifications"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type Repository struct{ tx pgx.Tx }

func New(tx pgx.Tx) *Repository { return &Repository{tx: tx} }

const broadcastSelect = `SELECT b.id, b.created_by, b.audience_kind, coalesce(b.partner_id, 0),
	b.telegram_ids, b.message_text, b.created_at, b.queued_at, b.status,
	count(recipient.user_id),
	count(message.id) FILTER (WHERE message.status IN ('pending', 'processing')),
	count(message.id) FILTER (WHERE message.status = 'sent'),
	count(message.id) FILTER (WHERE message.status = 'failed')
	FROM broadcasts b
	LEFT JOIN broadcast_recipients recipient ON recipient.broadcast_id = b.id
	LEFT JOIN outbox_messages message ON message.broadcast_id = recipient.broadcast_id
		AND message.user_id = recipient.user_id `

func (r *Repository) BroadcastByUpdate(ctx context.Context, updateID int64) (*entity.Broadcast, error) {
	return scanBroadcast(r.tx.QueryRow(ctx, broadcastSelect+`WHERE b.update_id = $1 GROUP BY b.id`, updateID))
}

func (r *Repository) Broadcast(ctx context.Context, id int64) (*entity.Broadcast, error) {
	return scanBroadcast(r.tx.QueryRow(ctx, broadcastSelect+`WHERE b.id = $1 GROUP BY b.id`, id))
}

func (r *Repository) Broadcasts(ctx context.Context) ([]entity.Broadcast, error) {
	rows, err := r.tx.Query(ctx, broadcastSelect+`GROUP BY b.id ORDER BY b.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list broadcasts: %w", err)
	}
	defer rows.Close()
	var values []entity.Broadcast
	for rows.Next() {
		value, err := scanBroadcast(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, *value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read broadcasts: %w", err)
	}
	return values, nil
}

func scanBroadcast(row pgx.Row) (*entity.Broadcast, error) {
	var value entity.Broadcast
	err := row.Scan(&value.ID, &value.CreatedBy, &value.Audience.Kind, &value.Audience.PartnerID,
		&value.Audience.TelegramIDs, &value.Text, &value.CreatedAt, &value.QueuedAt, &value.Status,
		&value.Total, &value.Pending, &value.Sent, &value.Failed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read broadcast: %w", err)
	}
	return &value, nil
}

// CreateBroadcast сохраняет черновик и снимок получателей одной SQL-командой.
// Пустая аудитория и неизвестный Telegram ID не оставляют частичного черновика.
func (r *Repository) CreateBroadcast(ctx context.Context, actor, updateID int64, audience entity.Audience, text string) (*entity.Broadcast, error) {
	ids := audience.TelegramIDs
	if ids == nil {
		ids = []int64{}
	}
	var id int64
	err := r.tx.QueryRow(ctx, `WITH recipients AS MATERIALIZED (
		SELECT users.id FROM users WHERE
		$1 = 'all' OR ($1 = 'users' AND users.telegram_user_id = ANY($3::bigint[])) OR
		($1 = 'partner' AND (
			EXISTS (SELECT 1 FROM promo_codes code WHERE code.user_id = users.id AND code.partner_id = $2)
			OR EXISTS (SELECT 1 FROM registrations registration JOIN partner_days day ON day.id = registration.partner_day_id
				WHERE registration.user_id = users.id AND day.partner_id = $2)))
	), created AS (
		INSERT INTO broadcasts(created_by, update_id, audience_kind, partner_id, telegram_ids, message_text)
		SELECT $4, $5, $1, nullif($2::bigint, 0), $3::bigint[], $6
		WHERE EXISTS (SELECT 1 FROM recipients)
		AND ($1 <> 'users' OR (SELECT count(*) FROM recipients) = cardinality($3::bigint[]))
		RETURNING id
	), saved AS (
		INSERT INTO broadcast_recipients(broadcast_id, user_id)
		SELECT created.id, recipients.id FROM created CROSS JOIN recipients
		RETURNING broadcast_id
	) SELECT broadcast_id FROM saved LIMIT 1`, audience.Kind, audience.PartnerID, ids, actor, updateID, text).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: аудитория пуста или часть Telegram ID еще не запускала бота", promotionadminentity.ErrInvalid)
	}
	if err != nil {
		return nil, fmt.Errorf("create broadcast: %w", err)
	}
	return r.Broadcast(ctx, id)
}

// QueueBroadcast атомарно меняет статус и создает ровно одну отправку каждому
// получателю снимка. Повторное подтверждение не создает новых сообщений.
func (r *Repository) QueueBroadcast(ctx context.Context, id int64) error {
	_, err := r.tx.Exec(ctx, `WITH queued AS (
		UPDATE broadcasts SET status = 'queued', queued_at = clock_timestamp()
		WHERE id = $1 AND status = 'draft' RETURNING id, message_text
	) INSERT INTO outbox_messages(user_id, broadcast_id, kind, payload, deduplication_key)
		SELECT recipient.user_id, queued.id, $2, jsonb_build_object('chat_id', users.chat_id, 'text', queued.message_text),
			'broadcast:' || queued.id || ':user:' || recipient.user_id
		FROM queued JOIN broadcast_recipients recipient ON recipient.broadcast_id = queued.id
		JOIN users ON users.id = recipient.user_id
		ORDER BY recipient.user_id`, id, telegram.MessageKindText)
	if err != nil {
		return fmt.Errorf("queue broadcast: %w", err)
	}
	return nil
}

func (r *Repository) CancelBroadcast(ctx context.Context, id int64) error {
	_, err := r.tx.Exec(ctx, `UPDATE broadcasts SET status = 'cancelled' WHERE id = $1 AND status = 'draft'`, id)
	if err != nil {
		return fmt.Errorf("cancel broadcast: %w", err)
	}
	return nil
}
