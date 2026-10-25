package broadcasts

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/notifications"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type BroadcastRepository interface {
	BroadcastByUpdate(context.Context, int64) (*entity.Broadcast, error)
	CreateBroadcast(context.Context, int64, int64, entity.Audience, string) (*entity.Broadcast, error)
	Broadcast(context.Context, int64) (*entity.Broadcast, error)
	Broadcasts(context.Context) ([]entity.Broadcast, error)
	QueueBroadcast(context.Context, int64) error
	CancelBroadcast(context.Context, int64) error
}

type BroadcastService struct {
	repository BroadcastRepository
	admin      *promotionadmin.AdminService
}

func NewBroadcasts(repository BroadcastRepository, admin *promotionadmin.AdminService) *BroadcastService {
	return &BroadcastService{repository: repository, admin: admin}
}

func (s *BroadcastService) Create(ctx context.Context, actor, updateID int64, audience entity.Audience, text string) (*entity.Broadcast, error) {
	if err := s.admin.Lock(ctx); err != nil {
		return nil, err
	}
	access, err := s.admin.Access(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !canBroadcast(access, audience) {
		return nil, promotionadmin.ErrForbidden
	}
	existing, err := s.repository.BroadcastByUpdate(ctx, updateID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.CreatedBy != actor || !canBroadcast(access, existing.Audience) {
			return nil, promotionadmin.ErrForbidden
		}
		return existing, nil
	}
	text = strings.TrimSpace(text)
	if updateID < 0 || text == "" || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') ||
		len(utf16.Encode([]rune(text))) > telegram.MaxMessageLength {
		return nil, fmt.Errorf("%w: нужен текст от 1 до 4096 символов Telegram", promotionadmin.ErrInvalid)
	}
	audience, err = normalizeAudience(audience)
	if err != nil {
		return nil, err
	}
	if audience.Kind == entity.AudiencePartner {
		if err := s.admin.AuthorizePartner(ctx, actor, audience.PartnerID); err != nil {
			return nil, err
		}
	}
	return s.repository.CreateBroadcast(ctx, actor, updateID, audience, text)
}

func (s *BroadcastService) View(ctx context.Context, actor, id int64) (*entity.Broadcast, error) {
	access, err := s.admin.Access(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !access.Organizer && len(access.PartnerIDs) == 0 {
		return nil, promotionadmin.ErrForbidden
	}
	value, err := s.repository.Broadcast(ctx, id)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, promotionadmin.ErrNotFound
	}
	if !canBroadcast(access, value.Audience) {
		return nil, promotionadmin.ErrForbidden
	}
	return value, nil
}

func (s *BroadcastService) List(ctx context.Context, actor int64) ([]entity.Broadcast, error) {
	access, err := s.admin.Access(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !access.Organizer && len(access.PartnerIDs) == 0 {
		return nil, promotionadmin.ErrForbidden
	}
	values, err := s.repository.Broadcasts(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(values, func(value entity.Broadcast) bool { return !canBroadcast(access, value.Audience) }), nil
}

func (s *BroadcastService) Send(ctx context.Context, actor, id int64) (*entity.Broadcast, error) {
	if err := s.admin.Lock(ctx); err != nil {
		return nil, err
	}
	value, err := s.View(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if value.Status == entity.BroadcastCancelled {
		return nil, fmt.Errorf("%w: рассылка отменена", promotionadmin.ErrConflict)
	}
	if value.Status == entity.BroadcastDraft {
		if err := s.repository.QueueBroadcast(ctx, id); err != nil {
			return nil, err
		}
		return s.View(ctx, actor, id)
	}
	return value, nil
}

func (s *BroadcastService) Cancel(ctx context.Context, actor, id int64) (*entity.Broadcast, error) {
	if err := s.admin.Lock(ctx); err != nil {
		return nil, err
	}
	value, err := s.View(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if value.Status == entity.BroadcastQueued {
		return nil, fmt.Errorf("%w: рассылка уже поставлена в очередь", promotionadmin.ErrConflict)
	}
	if value.Status == entity.BroadcastDraft {
		if err := s.repository.CancelBroadcast(ctx, id); err != nil {
			return nil, err
		}
		return s.View(ctx, actor, id)
	}
	return value, nil
}

func canBroadcast(access promotionadminentity.Access, audience entity.Audience) bool {
	return access.Organizer || (audience.Kind == entity.AudiencePartner && slices.Contains(access.PartnerIDs, audience.PartnerID))
}

func normalizeAudience(value entity.Audience) (entity.Audience, error) {
	switch value.Kind {
	case entity.AudienceAll:
		return entity.Audience{Kind: value.Kind}, nil
	case entity.AudiencePartner:
		if value.PartnerID > 0 {
			return entity.Audience{Kind: value.Kind, PartnerID: value.PartnerID}, nil
		}
	case entity.AudienceUsers:
		ids := slices.Clone(value.TelegramIDs)
		slices.Sort(ids)
		ids = slices.Compact(ids)
		if len(ids) > 0 && ids[0] > 0 {
			return entity.Audience{Kind: value.Kind, TelegramIDs: ids}, nil
		}
	}
	return entity.Audience{}, fmt.Errorf("%w: выберите аудиторию рассылки и корректные Telegram ID", promotionadmin.ErrInvalid)
}
