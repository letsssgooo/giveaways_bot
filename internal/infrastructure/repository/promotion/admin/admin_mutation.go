package admin

import (
	"context"
	"fmt"

	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
)

// Сохранение проходит в savepoint родительской транзакции обновления Telegram.
// Ошибка пользовательского ввода не должна оставлять частично измененные призы
// или помешать записать понятный ответ в outbox этой же транзакции.
func adminMutation[T any](ctx context.Context, repository *Repository, apply func(*Repository) (T, error)) (T, error) {
	var zero T
	tx, err := repository.tx.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin administration change: %w", err)
	}
	result, err := apply(New(tx))
	if err != nil {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			return zero, fmt.Errorf("rollback administration change: %w", rollbackErr)
		}
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit administration change: %w", err)
	}
	return result, nil
}

func (r *Repository) UpdateCampaign(ctx context.Context, value promotionadminentity.CampaignConfig) error {
	_, err := adminMutation(ctx, r, func(nested *Repository) (bool, error) { return true, nested.updateCampaign(ctx, value) })
	return err
}

func (r *Repository) UpdateMaterials(ctx context.Context, value promotionadminentity.Materials) error {
	_, err := adminMutation(ctx, r, func(nested *Repository) (bool, error) { return true, nested.updateMaterials(ctx, value) })
	return err
}

func (r *Repository) UpdatePolicies(ctx context.Context, value promotionadminentity.Policies) error {
	_, err := adminMutation(ctx, r, func(nested *Repository) (bool, error) { return true, nested.updatePolicies(ctx, value) })
	return err
}

func (r *Repository) UpdatePartner(ctx context.Context, value promotionadminentity.PartnerConfig) (int64, error) {
	return adminMutation(ctx, r, func(nested *Repository) (int64, error) { return nested.updatePartner(ctx, value) })
}

func (r *Repository) UpdateDay(ctx context.Context, value promotionadminentity.DayConfig) (int64, error) {
	return adminMutation(ctx, r, func(nested *Repository) (int64, error) { return nested.updateDay(ctx, value) })
}

func (r *Repository) UpdateDraw(ctx context.Context, value promotionadminentity.DrawConfig) (int64, error) {
	return adminMutation(ctx, r, func(nested *Repository) (int64, error) { return nested.updateDraw(ctx, value) })
}

func (r *Repository) UpdateGrant(ctx context.Context, telegramID, partnerID int64, enabled bool) error {
	_, err := adminMutation(ctx, r, func(nested *Repository) (bool, error) {
		return true, nested.updateGrant(ctx, telegramID, partnerID, enabled)
	})
	return err
}
