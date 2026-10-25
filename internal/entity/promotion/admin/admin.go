package admin

import (
	"errors"
	"time"
)

var (
	ErrForbidden = errors.New("недостаточно прав")
	ErrInvalid   = errors.New("некорректные данные")
	ErrNotFound  = errors.New("запись не найдена")
	ErrConflict  = errors.New("изменение противоречит существующим данным")
)

type Access struct {
	Organizer  bool
	PartnerIDs []int64
}

type Grant struct {
	TelegramID int64
	PartnerID  int64 // Ноль означает организатора.
	FromConfig bool
}

type CampaignConfig struct {
	Name             string
	StartsOn, EndsOn time.Time
}

type Materials struct {
	Rules, Instructions, BannerRef string
	ConsentText, ConsentVersion    string
}

// Нулевые значения означают, что организатор еще не настроил выдачу призов.
type Policies struct {
	WinnerResponseHours int
	ResponseStartsOn    string // delivered или approved.
	UndeliverablePolicy string // manual или reserve.
}

type PartnerConfig struct {
	ID              int64
	Name            string
	TelegramChatID  int64
	SubscriptionURL string
}

type DayConfig struct {
	ID, PartnerID           int64
	Date, OpensAt, ClosesAt time.Time
	PromoInstructions       string
	PromoValidUntil         *time.Time
	IsEnabled               bool
}

type PrizeConfig struct {
	Name, Instructions          string
	PlacesCount, UnitsPerWinner int
}

type DrawConfig struct {
	ID, PartnerDayID int64
	Kind             string
	ScheduledAt      time.Time
	Prizes           []PrizeConfig
}
