// Package draw содержит сценарии розыгрышей, утверждения результатов и получения призов.
package draw

import (
	"context"
	"slices"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
)

type Repository interface {
	LockAdmin(context.Context) error
	List(context.Context) ([]entity.Summary, error)
	View(context.Context, int64) (*entity.Result, error)
	Approve(context.Context, int64, int64) (*entity.Result, error)
}

type AccessProvider interface {
	Access(context.Context, int64) (promotionadminentity.Access, error)
}

type Service struct {
	repository Repository
	access     AccessProvider
}

func New(repository Repository, access AccessProvider) *Service {
	return &Service{repository: repository, access: access}
}

func (s *Service) List(ctx context.Context, actor int64) ([]entity.Summary, error) {
	access, err := s.access.Access(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !access.Organizer && len(access.PartnerIDs) == 0 {
		return nil, promotionadminentity.ErrForbidden
	}
	draws, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]entity.Summary, 0, len(draws))
	for _, draw := range draws {
		if allowed(access, draw) {
			visible = append(visible, draw)
		}
	}
	return visible, nil
}

func (s *Service) View(ctx context.Context, actor, drawID int64) (*entity.Result, error) {
	if drawID <= 0 {
		return nil, promotionadminentity.ErrInvalid
	}
	access, err := s.access.Access(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !access.Organizer && len(access.PartnerIDs) == 0 {
		return nil, promotionadminentity.ErrForbidden
	}
	result, err := s.repository.View(ctx, drawID)
	if err != nil {
		return nil, err
	}
	if !allowed(access, result.Summary) {
		return nil, promotionadminentity.ErrForbidden
	}
	return result, nil
}

func (s *Service) Approve(ctx context.Context, actor, drawID int64) (*entity.Result, error) {
	if drawID <= 0 {
		return nil, promotionadminentity.ErrInvalid
	}
	// Этот же замок используют изменения административных прав. Проверка
	// выполняется заново при подтверждении, а не только при показе кнопки.
	if err := s.repository.LockAdmin(ctx); err != nil {
		return nil, err
	}
	if _, err := s.View(ctx, actor, drawID); err != nil {
		return nil, err
	}
	return s.repository.Approve(ctx, actor, drawID)
}

func allowed(access promotionadminentity.Access, draw entity.Summary) bool {
	return access.Organizer || (draw.Kind == "daily" && slices.Contains(access.PartnerIDs, draw.PartnerID))
}
