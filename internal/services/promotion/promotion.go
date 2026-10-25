// Package promotion содержит сценарии управления акцией, партнерами и материалами.
package promotion

import (
	"context"
	"fmt"

	"github.com/letsssgooo/giveaways_bot/internal/entity/participation"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion"
	chancesentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/chances"
)

type Repository interface {
	Current(ctx context.Context) (*entity.Promotion, error)
	AvailablePartners(ctx context.Context) ([]entity.PartnerDay, error)
	Chances(ctx context.Context, userID int64) (chancesentity.Chances, error)
}

type Service struct {
	repository Repository
}

func New(repository Repository) *Service {
	return &Service{repository: repository}
}

// Current возвращает материалы акции и до начала, и после окончания приема.
func (s *Service) Current(ctx context.Context) (*entity.Promotion, error) {
	promotion, err := s.repository.Current(ctx)
	if err != nil {
		return nil, fmt.Errorf("load promotion: %w", err)
	}
	return promotion, nil
}

func (s *Service) AvailablePartners(ctx context.Context) ([]entity.PartnerDay, error) {
	partners, err := s.repository.AvailablePartners(ctx)
	if err != nil {
		return nil, fmt.Errorf("load available partners: %w", err)
	}
	return partners, nil
}

// Chances не меняет активность: просмотр меню не добавляет шанс.
func (s *Service) Chances(ctx context.Context, userID int64) (chancesentity.Chances, error) {
	chances, err := s.repository.Chances(ctx, userID)
	if err != nil {
		return chancesentity.Chances{}, fmt.Errorf("load chances: %w", err)
	}
	chances.Count = min(len(chances.ActiveDays), participation.MaxFinalChances)
	return chances, nil
}
