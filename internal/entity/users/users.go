// Package users содержит предметные модели пользователей.
package users

import "time"

// User хранит идентификаторы участника и время первого обращения к боту.
// Идентификацию пользователя выполняет Telegram.
type User struct {
	ID         int64
	TelegramID int64
	ChatID     int64
	StartedAt  time.Time
}
