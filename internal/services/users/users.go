// Package users содержит сценарии работы с пользователями.
package users

import (
	"context"
	"errors"
	"fmt"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/users"
)

var ErrInvalidIdentity = errors.New("invalid user identity")

type Repository interface {
	Ensure(ctx context.Context, telegramID, chatID int64) (entity.User, error)
}

type Service struct {
	repository Repository
}

func New(repository Repository) *Service {
	return &Service{repository: repository}
}

// Ensure сохраняет пользователя при первом обращении и возвращает существующего
// при повторном. Регистрация в розыгрыше выполняется отдельным сценарием.
func (s *Service) Ensure(ctx context.Context, telegramID, chatID int64) (entity.User, error) {
	if telegramID <= 0 || chatID <= 0 {
		return entity.User{}, ErrInvalidIdentity
	}
	user, err := s.repository.Ensure(ctx, telegramID, chatID)
	if err != nil {
		return entity.User{}, fmt.Errorf("ensure user: %w", err)
	}
	return user, nil
}
