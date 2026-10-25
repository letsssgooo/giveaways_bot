// Package draw содержит предметные модели розыгрышей, результатов и назначения призов.
package draw

import (
	"errors"
	"time"
)

var (
	ErrNotDue   = errors.New("время розыгрыша еще не наступило")
	ErrNotReady = errors.New("результаты розыгрыша еще не готовы")
)

const FinalReserveCount = 5

type Summary struct {
	ID, PartnerDayID, PartnerID                                            int64
	Kind, PartnerName                                                      string
	Date, ScheduledAt                                                      time.Time
	PerformedAt, ApprovedAt                                                *time.Time
	ApprovedBy                                                             int64
	ParticipantCount, WinnerCount, ReserveCount, PlacesCount, ReserveLimit int
}

type Participant struct {
	ID, UserID, TelegramID int64
	Weight                 int
}

type Selection struct {
	UserID, TelegramID, ParticipantID, PrizeID int64
	Position, PlaceNumber, UnitsPerWinner      int
	PrizeName                                  string
}

type Result struct {
	Summary
	Participants      []Participant
	Winners, Reserves []Selection
}
