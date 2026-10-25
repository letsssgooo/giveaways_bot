package admin

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
)

var (
	ErrForbidden = promotionadminentity.ErrForbidden
	ErrInvalid   = promotionadminentity.ErrInvalid
	ErrNotFound  = promotionadminentity.ErrNotFound
	ErrConflict  = promotionadminentity.ErrConflict
)

type AdminRepository interface {
	LockAdmin(context.Context) error
	AdminAccess(context.Context, int64) (promotionadminentity.Access, error)
	AdminCampaign(context.Context) (*promotionadminentity.CampaignConfig, error)
	UpdateCampaign(context.Context, promotionadminentity.CampaignConfig) error
	AdminMaterials(context.Context) (promotionadminentity.Materials, error)
	UpdateMaterials(context.Context, promotionadminentity.Materials) error
	AdminPolicies(context.Context) (promotionadminentity.Policies, error)
	UpdatePolicies(context.Context, promotionadminentity.Policies) error
	AdminPartners(context.Context) ([]promotionadminentity.PartnerConfig, error)
	UpdatePartner(context.Context, promotionadminentity.PartnerConfig) (int64, error)
	AdminDays(context.Context) ([]promotionadminentity.DayConfig, error)
	UpdateDay(context.Context, promotionadminentity.DayConfig) (int64, error)
	AdminDraws(context.Context) ([]promotionadminentity.DrawConfig, error)
	UpdateDraw(context.Context, promotionadminentity.DrawConfig) (int64, error)
	AdminFinalReserveCount(context.Context) (int, error)
	UpdateFinalReserveCount(context.Context, int) error
	AdminGrants(context.Context) ([]promotionadminentity.Grant, error)
	UpdateGrant(context.Context, int64, int64, bool) error
}

type AdminService struct {
	repository   AdminRepository
	organizerIDs map[int64]struct{}
}

func NewAdmin(repository AdminRepository, organizerIDs []int64) *AdminService {
	service := &AdminService{repository: repository, organizerIDs: make(map[int64]struct{}, len(organizerIDs))}
	for _, id := range organizerIDs {
		if id > 0 {
			service.organizerIDs[id] = struct{}{}
		}
	}
	return service
}

func (s *AdminService) Access(ctx context.Context, actor int64) (promotionadminentity.Access, error) {
	if actor <= 0 {
		return promotionadminentity.Access{}, ErrForbidden
	}
	if _, ok := s.organizerIDs[actor]; ok {
		return promotionadminentity.Access{Organizer: true}, nil
	}
	return s.repository.AdminAccess(ctx, actor)
}

func (s *AdminService) requireAccess(ctx context.Context, actor int64, organizer, lock bool) (promotionadminentity.Access, error) {
	if lock {
		if err := s.repository.LockAdmin(ctx); err != nil {
			return promotionadminentity.Access{}, err
		}
	}
	access, err := s.Access(ctx, actor)
	if err != nil {
		return promotionadminentity.Access{}, err
	}
	if !access.Organizer && (organizer || len(access.PartnerIDs) == 0) {
		return promotionadminentity.Access{}, ErrForbidden
	}
	return access, nil
}

// AuthorizePartner удерживает общий транзакционный замок с изменениями прав.
// Импорт вызывает этот метод в той же транзакции, в которой сохраняет коды.
func (s *AdminService) AuthorizePartner(ctx context.Context, actor, partnerID int64) error {
	access, err := s.requireAccess(ctx, actor, false, true)
	if err != nil {
		return err
	}
	if !access.Organizer && !slices.Contains(access.PartnerIDs, partnerID) {
		return ErrForbidden
	}
	partners, err := s.repository.AdminPartners(ctx)
	if err != nil {
		return err
	}
	for _, partner := range partners {
		if partner.ID == partnerID {
			return nil
		}
	}
	return ErrNotFound
}

func (s *AdminService) Campaign(ctx context.Context, actor int64) (*promotionadminentity.CampaignConfig, error) {
	if _, err := s.requireAccess(ctx, actor, false, false); err != nil {
		return nil, err
	}
	return s.repository.AdminCampaign(ctx)
}

func (s *AdminService) SaveCampaign(ctx context.Context, actor int64, value promotionadminentity.CampaignConfig) error {
	if _, err := s.requireAccess(ctx, actor, true, true); err != nil {
		return err
	}
	value.Name = strings.TrimSpace(value.Name)
	if !validName(value.Name) || value.StartsOn.IsZero() || value.EndsOn.IsZero() || dateKey(value.StartsOn) > dateKey(value.EndsOn) {
		return invalid("укажите название и корректный период акции")
	}
	return s.repository.UpdateCampaign(ctx, value)
}

func (s *AdminService) Materials(ctx context.Context, actor int64) (promotionadminentity.Materials, error) {
	if _, err := s.requireAccess(ctx, actor, false, false); err != nil {
		return promotionadminentity.Materials{}, err
	}
	return s.repository.AdminMaterials(ctx)
}

func (s *AdminService) SaveMaterials(ctx context.Context, actor int64, value promotionadminentity.Materials) error {
	if _, err := s.requireAccess(ctx, actor, true, true); err != nil {
		return err
	}
	value.BannerRef = strings.TrimSpace(value.BannerRef)
	value.ConsentVersion = strings.TrimSpace(value.ConsentVersion)
	value.ConsentText = strings.TrimSpace(value.ConsentText)
	if (value.ConsentText == "") != (value.ConsentVersion == "") {
		return invalid("текст согласия и его версия задаются вместе")
	}
	if len(value.ConsentVersion) > 100 || !validBanner(value.BannerRef) {
		return invalid("проверьте версию согласия и ссылку на баннер")
	}
	return s.repository.UpdateMaterials(ctx, value)
}

func (s *AdminService) Policies(ctx context.Context, actor int64) (promotionadminentity.Policies, error) {
	if _, err := s.requireAccess(ctx, actor, false, false); err != nil {
		return promotionadminentity.Policies{}, err
	}
	return s.repository.AdminPolicies(ctx)
}

func (s *AdminService) SavePolicies(ctx context.Context, actor int64, value promotionadminentity.Policies) error {
	if _, err := s.requireAccess(ctx, actor, true, true); err != nil {
		return err
	}
	unset := value.WinnerResponseHours == 0 && value.ResponseStartsOn == "" && value.UndeliverablePolicy == ""
	if !unset && (value.WinnerResponseHours <= 0 || value.WinnerResponseHours > 24*365 ||
		(value.ResponseStartsOn != "delivered" && value.ResponseStartsOn != "approved") ||
		(value.UndeliverablePolicy != "manual" && value.UndeliverablePolicy != "reserve")) {
		return invalid("проверьте срок ответа, начало отсчета и действие при недоставке")
	}
	return s.repository.UpdatePolicies(ctx, value)
}

func (s *AdminService) Partners(ctx context.Context, actor int64) ([]promotionadminentity.PartnerConfig, error) {
	access, err := s.requireAccess(ctx, actor, false, false)
	if err != nil {
		return nil, err
	}
	values, err := s.repository.AdminPartners(ctx)
	if err != nil {
		return nil, err
	}
	if !access.Organizer {
		values = slices.DeleteFunc(values, func(v promotionadminentity.PartnerConfig) bool { return !slices.Contains(access.PartnerIDs, v.ID) })
	}
	return values, nil
}

func (s *AdminService) SavePartner(ctx context.Context, actor int64, value promotionadminentity.PartnerConfig) (int64, error) {
	if value.ID == 0 {
		if _, err := s.requireAccess(ctx, actor, true, true); err != nil {
			return 0, err
		}
	} else if err := s.AuthorizePartner(ctx, actor, value.ID); err != nil {
		return 0, err
	}
	value.Name, value.SubscriptionURL = strings.TrimSpace(value.Name), strings.TrimSpace(value.SubscriptionURL)
	link, err := url.Parse(value.SubscriptionURL)
	if value.ID < 0 || !validName(value.Name) || value.TelegramChatID >= 0 || err != nil ||
		link.Scheme != "https" || link.Host != "t.me" || len(link.Path) <= 1 || link.User != nil || link.RawQuery != "" || link.Fragment != "" {
		return 0, invalid("нужны название, отрицательный ID канала и ссылка https://t.me/…")
	}
	return s.repository.UpdatePartner(ctx, value)
}

func (s *AdminService) Days(ctx context.Context, actor int64) ([]promotionadminentity.DayConfig, error) {
	access, err := s.requireAccess(ctx, actor, false, false)
	if err != nil {
		return nil, err
	}
	values, err := s.repository.AdminDays(ctx)
	if err != nil {
		return nil, err
	}
	if !access.Organizer {
		values = slices.DeleteFunc(values, func(v promotionadminentity.DayConfig) bool { return !slices.Contains(access.PartnerIDs, v.PartnerID) })
	}
	return values, nil
}

func (s *AdminService) SaveDay(ctx context.Context, actor int64, value promotionadminentity.DayConfig) (int64, error) {
	if err := s.AuthorizePartner(ctx, actor, value.PartnerID); err != nil {
		return 0, err
	}
	if value.ID != 0 {
		days, err := s.repository.AdminDays(ctx)
		if err != nil {
			return 0, err
		}
		found := false
		for _, day := range days {
			if day.ID == value.ID {
				found = true
				if err := s.AuthorizePartner(ctx, actor, day.PartnerID); err != nil {
					return 0, err
				}
				break
			}
		}
		if !found {
			return 0, ErrNotFound
		}
	}
	date := value.Date.Format(time.DateOnly)
	day, err := time.ParseInLocation(time.DateOnly, date, moscow)
	if err != nil || value.Date.IsZero() || value.ID < 0 || !value.OpensAt.Before(value.ClosesAt) ||
		value.OpensAt.In(moscow).Format(time.DateOnly) != date || value.ClosesAt.After(day.AddDate(0, 0, 1)) ||
		(value.PromoValidUntil != nil && value.PromoValidUntil.Before(value.OpensAt)) {
		return 0, invalid("проверьте дату, окно приема по Москве и срок действия промокода")
	}
	return s.repository.UpdateDay(ctx, value)
}

func (s *AdminService) Draws(ctx context.Context, actor int64) ([]promotionadminentity.DrawConfig, error) {
	access, err := s.requireAccess(ctx, actor, false, false)
	if err != nil {
		return nil, err
	}
	values, err := s.repository.AdminDraws(ctx)
	if err != nil || access.Organizer {
		return values, err
	}
	days, err := s.Days(ctx, actor)
	if err != nil {
		return nil, err
	}
	values = slices.DeleteFunc(values, func(v promotionadminentity.DrawConfig) bool {
		return !slices.ContainsFunc(days, func(day promotionadminentity.DayConfig) bool { return day.ID == v.PartnerDayID })
	})
	return values, nil
}

func (s *AdminService) SaveDraw(ctx context.Context, actor int64, value promotionadminentity.DrawConfig) (int64, error) {
	access, err := s.requireAccess(ctx, actor, value.Kind == "final", true)
	if err != nil {
		return 0, err
	}
	if value.Kind != "daily" && value.Kind != "final" {
		return 0, invalid("тип розыгрыша: daily или final")
	}
	if value.Kind == "daily" {
		days, err := s.repository.AdminDays(ctx)
		if err != nil {
			return 0, err
		}
		found := false
		for _, day := range days {
			if day.ID == value.PartnerDayID {
				found = true
				if err := s.AuthorizePartner(ctx, actor, day.PartnerID); err != nil {
					return 0, err
				}
				break
			}
		}
		if !found {
			return 0, ErrNotFound
		}
	}
	if value.ID != 0 {
		visible, err := s.Draws(ctx, actor)
		if err != nil {
			return 0, err
		}
		if !slices.ContainsFunc(visible, func(draw promotionadminentity.DrawConfig) bool { return draw.ID == value.ID }) {
			if !access.Organizer {
				return 0, ErrForbidden
			}
			return 0, ErrNotFound
		}
	}
	if value.ID < 0 || value.ScheduledAt.IsZero() || len(value.Prizes) == 0 || (value.Kind == "final" && value.PartnerDayID != 0) {
		return 0, invalid("укажите расписание и хотя бы один приз")
	}
	for i := range value.Prizes {
		value.Prizes[i].Name = strings.TrimSpace(value.Prizes[i].Name)
		prize := value.Prizes[i]
		if !validName(prize.Name) || prize.PlacesCount <= 0 || prize.UnitsPerWinner <= 0 || prize.PlacesCount > 100000 || prize.UnitsPerWinner > 100000 {
			return 0, invalid("у приза нужны название, число победителей и количество единиц на победителя")
		}
	}
	if value.Kind == "final" && (len(value.Prizes) != 1 || value.Prizes[0].PlacesCount != 5 || value.Prizes[0].UnitsPerWinner != 2) {
		return 0, invalid("финальный розыгрыш: пять победителей по два билета")
	}
	return s.repository.UpdateDraw(ctx, value)
}

func (s *AdminService) Grants(ctx context.Context, actor int64) ([]promotionadminentity.Grant, error) {
	if _, err := s.requireAccess(ctx, actor, true, false); err != nil {
		return nil, err
	}
	grants, err := s.repository.AdminGrants(ctx)
	if err != nil {
		return nil, err
	}
	grants = slices.DeleteFunc(grants, func(grant promotionadminentity.Grant) bool {
		_, configured := s.organizerIDs[grant.TelegramID]
		return configured && grant.PartnerID == 0
	})
	for telegramID := range s.organizerIDs {
		grants = append(grants, promotionadminentity.Grant{TelegramID: telegramID, FromConfig: true})
	}
	slices.SortFunc(grants, func(left, right promotionadminentity.Grant) int {
		if order := cmp.Compare(left.TelegramID, right.TelegramID); order != 0 {
			return order
		}
		return cmp.Compare(left.PartnerID, right.PartnerID)
	})
	return grants, nil
}

func (s *AdminService) Grant(ctx context.Context, actor, target, partnerID int64) error {
	return s.changeGrant(ctx, actor, target, partnerID, true)
}

func (s *AdminService) Revoke(ctx context.Context, actor, target, partnerID int64) error {
	return s.changeGrant(ctx, actor, target, partnerID, false)
}

func (s *AdminService) changeGrant(ctx context.Context, actor, target, partnerID int64, enabled bool) error {
	if _, err := s.requireAccess(ctx, actor, true, true); err != nil {
		return err
	}
	if target <= 0 || partnerID < 0 {
		return invalid("нужны положительный Telegram ID и ID партнера; 0 — организатор")
	}
	if !enabled && partnerID == 0 {
		if _, bootstrap := s.organizerIDs[target]; bootstrap {
			return fmt.Errorf("%w: доступ этого организатора задан в конфигурации", ErrConflict)
		}
	}
	if partnerID > 0 {
		if err := s.AuthorizePartner(ctx, actor, partnerID); err != nil {
			return err
		}
	}
	return s.repository.UpdateGrant(ctx, target, partnerID, enabled)
}

var moscow = time.FixedZone("Europe/Moscow", 3*60*60)

func dateKey(t time.Time) string   { return t.Format(time.DateOnly) }
func validName(value string) bool  { return value != "" && utf8.RuneCountInString(value) <= 200 }
func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }

func validBanner(value string) bool {
	if value == "" || value == "builtin:welcome" {
		return true
	}
	if len(value) > 2048 {
		return false
	}
	if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
		parsed, err := url.Parse(value)
		return err == nil && parsed.Hostname() != "" && parsed.User == nil
	}
	for _, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-'
		if !valid {
			return false
		}
	}
	return true
}
