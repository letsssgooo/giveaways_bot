// Package users содержит доступ к пользователям и правам в PostgreSQL.
package users

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/letsssgooo/giveaways_bot/internal/entity/users"
)

type Repository struct {
	tx pgx.Tx
}

func New(tx pgx.Tx) *Repository {
	return &Repository{tx: tx}
}

// Ensure обновляет адрес диалога, сохраняя время первого обращения пользователя.
func (r *Repository) Ensure(ctx context.Context, telegramID, chatID int64) (users.User, error) {
	user, err := r.find(ctx, telegramID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.tx.QueryRow(ctx, `INSERT INTO users(telegram_user_id, chat_id) VALUES ($1, $2)
			ON CONFLICT (telegram_user_id) DO NOTHING
			RETURNING id, telegram_user_id, chat_id, started_at`, telegramID, chatID).
			Scan(&user.ID, &user.TelegramID, &user.ChatID, &user.StartedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			// Другой запрос успел создать пользователя: следующий снимок его увидит.
			user, err = r.find(ctx, telegramID)
		}
	}
	if err != nil {
		return users.User{}, fmt.Errorf("ensure Telegram user: %w", err)
	}
	if user.ChatID == chatID {
		// Не удерживаем блокировку пользователя до блокировок акции и дня.
		return user, nil
	}
	err = r.tx.QueryRow(ctx, `UPDATE users SET chat_id = $2 WHERE telegram_user_id = $1
		RETURNING id, telegram_user_id, chat_id, started_at`, telegramID, chatID).
		Scan(&user.ID, &user.TelegramID, &user.ChatID, &user.StartedAt)
	if err != nil {
		return users.User{}, fmt.Errorf("save Telegram user: %w", err)
	}
	return user, nil
}

func (r *Repository) find(ctx context.Context, telegramID int64) (users.User, error) {
	var user users.User
	err := r.tx.QueryRow(ctx, `SELECT id, telegram_user_id, chat_id, started_at
		FROM users WHERE telegram_user_id = $1`, telegramID).
		Scan(&user.ID, &user.TelegramID, &user.ChatID, &user.StartedAt)
	return user, err
}

func (r *Repository) FindByTelegramID(ctx context.Context, telegramID int64) (users.User, error) {
	return r.find(ctx, telegramID)
}

// SaveUsername сохраняет имя из Telegram после Ensure. Пустое имя удаляет связь;
// неизменившееся значение не блокирует строку пользователя до конца транзакции.
func (r *Repository) SaveUsername(ctx context.Context, telegramID int64, username string) error {
	_, err := r.tx.Exec(ctx, `UPDATE users SET telegram_username = nullif($2, '')
		WHERE telegram_user_id = $1 AND telegram_username IS DISTINCT FROM nullif($2, '')`,
		telegramID, strings.ToLower(username))
	if err != nil {
		return fmt.Errorf("save Telegram username: %w", err)
	}
	return nil
}

// FindByUsername принимает имя без @. Кэш заполняется только при обращении к боту;
// отсутствующее или неоднозначное имя возвращает pgx.ErrNoRows.
// Права по-прежнему привязываются к постоянному Telegram ID найденного пользователя.
func (r *Repository) FindByUsername(ctx context.Context, username string) (users.User, error) {
	var user users.User
	err := r.tx.QueryRow(ctx, `SELECT id, telegram_user_id, chat_id, started_at FROM (
		SELECT id, telegram_user_id, chat_id, started_at, count(*) OVER () AS matches
		FROM users WHERE lower(telegram_username) = $1
	) AS candidates WHERE matches = 1`, strings.ToLower(username)).
		Scan(&user.ID, &user.TelegramID, &user.ChatID, &user.StartedAt)
	if err != nil {
		return users.User{}, fmt.Errorf("find Telegram username: %w", err)
	}
	return user, nil
}
