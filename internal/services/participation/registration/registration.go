// Package participation содержит сценарии выдачи кодов, регистрации и учета активных дней.
package registration

import (
	"context"
	"fmt"
	"time"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/participation"
	registrationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation/registration"
)

const subscriptionTimeout = 5 * time.Second

type RegistrationRepository interface {
	View(ctx context.Context, telegramID, dayID int64) (*registrationentity.RegistrationDay, error)
	Now(ctx context.Context) (time.Time, error)
	Lock(ctx context.Context, userID, telegramID, dayID int64) (*registrationentity.RegistrationDay, error)
	Save(ctx context.Context, userID, dayID int64, registeredAt, checkedAt time.Time) (bool, int, error)
}

type SubscriptionChecker interface {
	CheckSubscription(ctx context.Context, chatID, userID int64) (bool, error)
}

type RegistrationService struct {
	repository RegistrationRepository
	checker    SubscriptionChecker
}

func NewRegistration(repository RegistrationRepository, checker SubscriptionChecker) *RegistrationService {
	return &RegistrationService{repository: repository, checker: checker}
}

func (s *RegistrationService) View(ctx context.Context, telegramID, dayID int64) (*registrationentity.RegistrationDay, error) {
	day, err := s.repository.View(ctx, telegramID, dayID)
	if err != nil {
		return nil, fmt.Errorf("load registration day: %w", err)
	}
	if day != nil {
		day.Chances = min(day.Chances, entity.MaxFinalChances)
	}
	return day, nil
}

// Prepare не удерживает транзакцию и блокировки во время обращения к Telegram.
// Повторная регистрация не требует повторной проверки подписки.
func (s *RegistrationService) Prepare(ctx context.Context, telegramID, dayID int64) (registrationentity.Verification, error) {
	verification := registrationentity.Verification{DayID: dayID, TelegramID: telegramID}
	day, err := s.View(ctx, telegramID, dayID)
	if err != nil {
		return verification, err
	}
	if day == nil || day.Registered || !day.Open {
		return verification, nil
	}
	verification.TelegramChatID = day.TelegramChatID
	if s.checker == nil {
		verification.TechnicalFailure = true
		return verification, nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()
	subscribed, err := s.checker.CheckSubscription(checkCtx, day.TelegramChatID, telegramID)
	if ctx.Err() != nil {
		return verification, ctx.Err()
	}
	if err != nil {
		verification.TechnicalFailure = true
		return verification, nil
	}
	verification.CheckedAt, err = s.repository.Now(ctx)
	if err != nil {
		return verification, fmt.Errorf("timestamp subscription check: %w", err)
	}
	verification.Subscribed = subscribed
	return verification, nil
}

// Register вызывается в той же транзакции, что outbox и обработанный update.
// Под блокировками повторно проверяются окно приема и конфигурация партнера.
func (s *RegistrationService) Register(ctx context.Context, userID, telegramID, dayID int64, verification registrationentity.Verification) (registrationentity.RegistrationResult, error) {
	day, err := s.repository.Lock(ctx, userID, telegramID, dayID)
	if err != nil {
		return registrationentity.RegistrationResult{}, fmt.Errorf("lock registration day: %w", err)
	}
	result := registrationentity.RegistrationResult{Day: day}
	if day == nil {
		result.Status = registrationentity.Unavailable
		return result, nil
	}
	result.Chances = min(day.Chances, entity.MaxFinalChances)
	switch {
	case day.Registered:
		result.Status = registrationentity.AlreadyRegistered
	case !day.Open:
		result.Status = registrationentity.Closed
	case !validVerification(day, telegramID, verification):
		result.Status = registrationentity.VerificationFailed
	case !verification.Subscribed:
		result.Status = registrationentity.NotSubscribed
	default:
		created, chances, err := s.repository.Save(ctx, userID, dayID, day.Now, verification.CheckedAt)
		if err != nil {
			return registrationentity.RegistrationResult{}, fmt.Errorf("save registration: %w", err)
		}
		result.Status = registrationentity.Registered
		if !created {
			result.Status = registrationentity.AlreadyRegistered
		}
		result.Chances = min(chances, entity.MaxFinalChances)
	}
	return result, nil
}

func validVerification(day *registrationentity.RegistrationDay, telegramID int64, verification registrationentity.Verification) bool {
	return !verification.TechnicalFailure && verification.DayID == day.ID &&
		verification.TelegramID == telegramID && verification.TelegramChatID == day.TelegramChatID &&
		!verification.CheckedAt.IsZero() && !verification.CheckedAt.After(day.Now)
}
