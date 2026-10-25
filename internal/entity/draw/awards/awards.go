package awards

import (
	"errors"
	"time"
)

const (
	AwardPending   = "pending"
	AwardNotified  = "notified"
	AwardConfirmed = "confirmed"
	AwardManual    = "manual"
	AwardReplaced  = "replaced"
	AwardExhausted = "exhausted"
)

var (
	ErrAwardUnavailable = errors.New("получение этого приза недоступно")
	ErrClaimExpired     = errors.New("срок получения приза истек")
	ErrConsentRequired  = errors.New("сначала необходимо согласие на передачу данных")
)

type Award struct {
	PlaceNumber                                       int
	ID, DrawID, UserID, TelegramID, PartnerID         int64
	Kind, PrizeName, PartnerName, Instructions        string
	Units                                             int
	Current                                           bool
	State                                             string
	ResponseHours                                     int
	ResponseStartsOn                                  string
	Now                                               time.Time
	Deadline, NotifiedAt, ConfirmedAt, ConsentGivenAt *time.Time
	ConsentVersion, ConsentText, FirstName, LastName  string
}
