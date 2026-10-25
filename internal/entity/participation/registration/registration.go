package registration

import "time"

// RegistrationDay содержит сведения для приглашения и подтверждения участия.
type RegistrationDay struct {
	ID              int64
	Name            string
	TelegramChatID  int64
	SubscriptionURL string
	Date            time.Time
	ClosesAt        time.Time
	Now             time.Time
	Open            bool
	Registered      bool
	Chances         int
}

// Verification живет только во время обработки одного Telegram update.
// Проверка Telegram выполняется до транзакции регистрации.
type Verification struct {
	DayID            int64
	TelegramID       int64
	TelegramChatID   int64
	CheckedAt        time.Time
	Subscribed       bool
	TechnicalFailure bool
}

type RegistrationStatus string

const (
	Registered         RegistrationStatus = "registered"
	AlreadyRegistered  RegistrationStatus = "already_registered"
	Closed             RegistrationStatus = "closed"
	NotSubscribed      RegistrationStatus = "not_subscribed"
	VerificationFailed RegistrationStatus = "verification_failed"
	Unavailable        RegistrationStatus = "unavailable"
)

type RegistrationResult struct {
	Status  RegistrationStatus
	Day     *RegistrationDay
	Chances int
}
