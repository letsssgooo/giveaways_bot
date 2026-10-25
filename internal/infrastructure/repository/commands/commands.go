// Package commands сохраняет обновления меню Telegram в существующей очереди.
package commands

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type Repository struct{ tx pgx.Tx }

func New(tx pgx.Tx) *Repository { return &Repository{tx: tx} }

const enqueue = `INSERT INTO outbox_messages (deduplication_key,user_id,kind,payload)
	SELECT 'telegram:commands:' || gen_random_uuid()::text, users.id, $1,
		jsonb_build_object('chat_id',users.telegram_user_id)
	FROM users WHERE users.chat_id=users.telegram_user_id AND `

// Enqueue обновляет меню известного пользователя после изменения его ролей.
// Выдача прав сама по себе не создает пользователя, не обращавшегося к боту.
func (r *Repository) Enqueue(ctx context.Context, telegramID int64) error {
	_, err := r.tx.Exec(ctx, enqueue+`users.telegram_user_id=$2`, protocol.MessageKindCommands, telegramID)
	if err != nil {
		return fmt.Errorf("enqueue Telegram commands: %w", err)
	}
	return nil
}

// Ensure настраивает меню нового администратора и повторяет неудачное обновление
// при следующем обращении, включая сброс меню бывшего администратора.
func (r *Repository) Ensure(ctx context.Context, telegramID int64, organizerIDs []int64) error {
	_, err := r.tx.Exec(ctx, enqueue+`users.telegram_user_id=$2
		AND (users.telegram_user_id=ANY($3::bigint[])
			OR EXISTS(SELECT 1 FROM administration_grants WHERE telegram_user_id=users.telegram_user_id)
			OR EXISTS(SELECT 1 FROM outbox_messages AS message WHERE message.user_id=users.id AND message.kind=$1))
		AND coalesce((SELECT message.status='failed' FROM outbox_messages AS message
			WHERE message.user_id=users.id AND message.kind=$1 ORDER BY message.id DESC LIMIT 1),true)`,
		protocol.MessageKindCommands, telegramID, organizerIDs)
	if err != nil {
		return fmt.Errorf("initialize Telegram commands: %w", err)
	}
	return nil
}

// Startup учитывает текущие права и все ранее настроенные личные меню. Поэтому
// удаление организатора из конфигурации сбрасывает его меню после перезапуска.
func (r *Repository) Startup(ctx context.Context, organizerIDs []int64) error {
	_, err := r.tx.Exec(ctx, enqueue+`(users.telegram_user_id=ANY($2::bigint[])
		OR EXISTS(SELECT 1 FROM administration_grants WHERE telegram_user_id=users.telegram_user_id)
		OR EXISTS(SELECT 1 FROM outbox_messages AS message WHERE message.user_id=users.id AND message.kind=$1))`,
		protocol.MessageKindCommands, organizerIDs)
	if err != nil {
		return fmt.Errorf("refresh Telegram commands: %w", err)
	}
	return nil
}
