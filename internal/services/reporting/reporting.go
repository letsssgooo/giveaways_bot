// Package reporting содержит сценарии формирования статистики и выгрузок.
package reporting

import (
	"context"
	"fmt"
	"slices"

	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/reporting"
	reportcsv "github.com/letsssgooo/giveaways_bot/internal/services/reporting/csv"
)

var ErrExportTooLarge = entity.ErrExportTooLarge

type Repository interface {
	LockAdmin(context.Context) error
	Partners(context.Context, entity.Scope) ([]entity.Partner, error)
	Stats(context.Context, entity.Scope) (entity.Stats, error)
	Participants(context.Context, entity.Scope) ([]entity.Participant, error)
	Winners(context.Context, entity.Scope) ([]entity.Winner, error)
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

func (s *Service) Partners(ctx context.Context, actor int64) ([]entity.Partner, error) {
	scope, err := s.scope(ctx, actor, 0)
	if err != nil {
		return nil, err
	}
	return s.repository.Partners(ctx, scope)
}

func (s *Service) Stats(ctx context.Context, actor, partnerID int64) (entity.Stats, error) {
	scope, err := s.scope(ctx, actor, partnerID)
	if err != nil {
		return entity.Stats{}, err
	}
	return s.repository.Stats(ctx, scope)
}

func (s *Service) Authorize(ctx context.Context, actor, partnerID int64) error {
	_, err := s.scope(ctx, actor, partnerID)
	return err
}

func (s *Service) Participants(ctx context.Context, actor, partnerID int64) ([]entity.Participant, error) {
	scope, err := s.scope(ctx, actor, partnerID)
	if err != nil {
		return nil, err
	}
	return s.repository.Participants(ctx, scope)
}

func (s *Service) Winners(ctx context.Context, actor, partnerID int64) ([]entity.Winner, error) {
	scope, err := s.scope(ctx, actor, partnerID)
	if err != nil {
		return nil, err
	}
	return s.repository.Winners(ctx, scope)
}

func (s *Service) Export(ctx context.Context, actor, partnerID int64, kind string) (entity.Document, error) {
	var data []byte
	var count int
	var caption string
	var err error
	switch kind {
	case "participants":
		rows, readErr := s.Participants(ctx, actor, partnerID)
		if readErr != nil {
			return entity.Document{}, readErr
		}
		data, err = reportcsv.ParticipantsCSV(rows)
		count = len(rows)
		caption = "Участники"
	case "winners":
		rows, readErr := s.Winners(ctx, actor, partnerID)
		if readErr != nil {
			return entity.Document{}, readErr
		}
		data, err = reportcsv.WinnersCSV(rows)
		count = len(rows)
		caption = "Результаты розыгрышей"
	default:
		return entity.Document{}, promotionadminentity.ErrInvalid
	}
	if err != nil {
		return entity.Document{}, err
	}
	filename := kind + ".csv"
	if partnerID > 0 {
		filename = fmt.Sprintf("%s-partner-%d.csv", kind, partnerID)
	}
	return entity.Document{Filename: filename, Data: data, Caption: fmt.Sprintf("%s: %d записей.", caption, count)}, nil
}

// Общий замок с изменениями прав удерживается до конца транзакции. Поэтому
// отзыв доступа не может вклиниться между проверкой и чтением выгрузки.
func (s *Service) scope(ctx context.Context, actor, partnerID int64) (entity.Scope, error) {
	if partnerID < 0 {
		return entity.Scope{}, promotionadminentity.ErrInvalid
	}
	if err := s.repository.LockAdmin(ctx); err != nil {
		return entity.Scope{}, err
	}
	if actor <= 0 {
		return entity.Scope{}, promotionadminentity.ErrForbidden
	}
	access, err := s.access.Access(ctx, actor)
	if err != nil {
		return entity.Scope{}, err
	}
	if !access.Organizer && len(access.PartnerIDs) == 0 {
		return entity.Scope{}, promotionadminentity.ErrForbidden
	}
	scope := entity.Scope{AllPartners: access.Organizer, IncludeUsers: access.Organizer, PartnerIDs: slices.Clone(access.PartnerIDs)}
	if partnerID > 0 {
		if !access.Organizer && !slices.Contains(access.PartnerIDs, partnerID) {
			return entity.Scope{}, promotionadminentity.ErrForbidden
		}
		scope = entity.Scope{PartnerIDs: []int64{partnerID}}
		partners, err := s.repository.Partners(ctx, scope)
		if err != nil {
			return entity.Scope{}, err
		}
		if len(partners) == 0 {
			return entity.Scope{}, promotionadminentity.ErrNotFound
		}
	}
	return scope, nil
}
