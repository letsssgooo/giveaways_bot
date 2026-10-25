// Package promotion содержит предметные модели акции, партнеров и календаря.
package promotion

import "time"

type Promotion struct {
	Name         string
	Rules        string
	Instructions string
	BannerRef    string
	StartsOn     time.Time
	EndsOn       time.Time
}

// PartnerDay — отдельный день партнера с открытым приемом участников.
type PartnerDay struct {
	ID        int64
	PartnerID int64
	Name      string
	Date      time.Time
	OpensAt   time.Time
	ClosesAt  time.Time
}
