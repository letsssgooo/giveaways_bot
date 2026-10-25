// Package participation содержит сценарии выдачи кодов, регистрации и учета активных дней.
package participation

import (
	"context"
	"errors"
	"fmt"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/participation"
)

var ErrInvalidRequest = errors.New("invalid promo code request")

type Repository interface {
	CodeOptions(ctx context.Context, userID int64) ([]entity.CodeOption, error)
	RequestCode(ctx context.Context, userID, partnerDayID, updateID int64) (entity.CodeResult, error)
}

type Service struct {
	repository Repository
}

func New(repository Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) CodeOptions(ctx context.Context, userID int64) ([]entity.CodeOption, error) {
	if userID <= 0 {
		return nil, ErrInvalidRequest
	}
	options, err := s.repository.CodeOptions(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load promo code options: %w", err)
	}
	return options, nil
}

func (s *Service) RequestCode(ctx context.Context, userID, partnerDayID, updateID int64) (entity.CodeResult, error) {
	if userID <= 0 || partnerDayID <= 0 || updateID < 0 {
		return entity.CodeResult{}, ErrInvalidRequest
	}
	result, err := s.repository.RequestCode(ctx, userID, partnerDayID, updateID)
	if err != nil {
		return entity.CodeResult{}, fmt.Errorf("request promo code: %w", err)
	}
	return result, nil
}
