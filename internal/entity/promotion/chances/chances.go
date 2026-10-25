package chances

import "time"

// Chances — фестивальный пропуск пользователя. История сохраняется целиком,
// даже когда количество шансов достигло предела.
type Chances struct {
	Configured    bool
	Count         int
	ActiveDays    []ActivityDay
	RemainingDays int
	Prizes        []FinalPrize
}

type ActivityDay struct {
	Date     time.Time
	Partners []string
}

type FinalPrize struct {
	Name           string
	PlacesCount    int
	UnitsPerWinner int
}
