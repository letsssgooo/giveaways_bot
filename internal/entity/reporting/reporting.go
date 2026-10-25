// Package reporting содержит предметные модели статистики и отчетов.
package reporting

import (
	"errors"
	"time"
)

const (
	MaxExportRows  = 100000
	MaxExportBytes = 8 << 20
)

var ErrExportTooLarge = errors.New("выгрузка слишком большая: выберите отдельного партнера")

type Partner struct {
	ID   int64
	Name string
}

type DailyUsers struct {
	Date  time.Time
	Count int64
}

type PartnerStats struct {
	Partner
	Requests, Issued, Remaining, Registrations int64
}

type DayStats struct {
	PartnerID, PartnerDayID int64
	PartnerName             string
	Date                    time.Time
	Registrations           int64
}

type Stats struct {
	GlobalUsersVisible                                        bool
	TotalUsers                                                int64
	NewUsers                                                  []DailyUsers
	PromoRequests, PromoIssued, PromoRemaining, Registrations int64
	Partners                                                  []PartnerStats
	Days                                                      []DayStats
}

type Participant struct {
	TelegramID, PartnerID, PartnerDayID, DrawID int64
	PartnerName, DrawKind                       string
	Date                                        time.Time
	RegisteredAt                                *time.Time
	Weight                                      int
}

type Winner struct {
	DrawID, PartnerID, PartnerDayID, TelegramID, PrizeID, AwardID      int64
	DrawKind, PartnerName, Role, PrizeName, AwardStatus                string
	ConsentVersion, FirstName, LastName                                string
	Date                                                               time.Time
	RegisteredAt, PerformedAt, ApprovedAt, ConfirmedAt, ConsentGivenAt *time.Time
	Weight, Position, PlaceNumber, Units                               int
	AwardCurrent                                                       bool
}

type Document struct {
	Filename string
	Data     []byte
	Caption  string
}

// Scope задается сервисом после проверки доступа; пустой список не означает все.
type Scope struct {
	AllPartners  bool
	PartnerIDs   []int64
	IncludeUsers bool
}
