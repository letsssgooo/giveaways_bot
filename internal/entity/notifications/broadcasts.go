// Package notifications содержит предметные модели рассылок, уведомлений и состояний доставки.
package notifications

import "time"

const (
	AudienceAll     = "all"
	AudiencePartner = "partner"
	AudienceUsers   = "users"

	BroadcastDraft     = "draft"
	BroadcastQueued    = "queued"
	BroadcastCancelled = "cancelled"
)

type Audience struct {
	Kind        string  `json:"kind"`
	PartnerID   int64   `json:"partner_id,omitempty"`
	TelegramIDs []int64 `json:"telegram_ids,omitempty"`
}

type Broadcast struct {
	ID        int64
	CreatedBy int64 // Telegram ID администратора, создавшего рассылку.
	Audience  Audience
	Text      string
	CreatedAt time.Time
	QueuedAt  *time.Time
	Status    string
	Total     int
	Pending   int
	Sent      int
	Failed    int
}
