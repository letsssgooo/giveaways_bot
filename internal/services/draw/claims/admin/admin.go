package admin

import (
	"context"
	"slices"

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
)

type AwardAdministrationRepository interface {
	ViewAdmin(context.Context, int64) (*awardsentity.Award, error)
	ListIssues(context.Context) ([]awardsentity.Award, error)
	Retry(context.Context, int64) error
	Replace(context.Context, int64) error
}

type AwardAccess interface {
	Access(context.Context, int64) (promotionadminentity.Access, error)
}

type AdministrationLock interface {
	LockAdmin(context.Context) error
}

type ClaimAdministration struct {
	repository AwardAdministrationRepository
	access     AwardAccess
	lock       AdministrationLock
}

func NewClaimAdministration(repository AwardAdministrationRepository, access AwardAccess, lock AdministrationLock) *ClaimAdministration {
	return &ClaimAdministration{repository: repository, access: access, lock: lock}
}

func (s *ClaimAdministration) Issues(ctx context.Context, actorID int64) ([]awardsentity.Award, error) {
	access, err := s.access.Access(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !access.Organizer && len(access.PartnerIDs) == 0 {
		return nil, promotionadminentity.ErrForbidden
	}
	awards, err := s.repository.ListIssues(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(awards, func(award awardsentity.Award) bool { return !canManageAward(access, award) }), nil
}

func (s *ClaimAdministration) View(ctx context.Context, actorID, awardID int64) (*awardsentity.Award, error) {
	// Тот же замок используют назначения и отзыв прав в админке.
	if err := s.lock.LockAdmin(ctx); err != nil {
		return nil, err
	}
	access, err := s.access.Access(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !access.Organizer && len(access.PartnerIDs) == 0 {
		return nil, promotionadminentity.ErrForbidden
	}
	award, err := s.repository.ViewAdmin(ctx, awardID)
	if err != nil {
		return nil, err
	}
	if award == nil {
		return nil, awardsentity.ErrAwardUnavailable
	}
	if !canManageAward(access, *award) {
		return nil, promotionadminentity.ErrForbidden
	}
	return award, nil
}

func (s *ClaimAdministration) Retry(ctx context.Context, actorID, awardID int64) error {
	if _, err := s.View(ctx, actorID, awardID); err != nil {
		return err
	}
	return s.repository.Retry(ctx, awardID)
}

func (s *ClaimAdministration) Replace(ctx context.Context, actorID, awardID int64) error {
	if _, err := s.View(ctx, actorID, awardID); err != nil {
		return err
	}
	return s.repository.Replace(ctx, awardID)
}

func canManageAward(access promotionadminentity.Access, award awardsentity.Award) bool {
	return access.Organizer || (award.Kind == "daily" && slices.Contains(access.PartnerIDs, award.PartnerID))
}
