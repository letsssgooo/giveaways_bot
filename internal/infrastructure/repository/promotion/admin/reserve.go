package admin

import "context"

func (r *Repository) AdminFinalReserveCount(ctx context.Context) (int, error) {
	var count int
	err := r.tx.QueryRow(ctx, `SELECT coalesce((SELECT reserve_count FROM draws WHERE kind='final' AND promotion_id=1),5)`).Scan(&count)
	return count, adminError(err)
}

func (r *Repository) UpdateFinalReserveCount(ctx context.Context, count int) error {
	result, err := r.tx.Exec(ctx, `UPDATE draws SET reserve_count=$1
		WHERE kind='final' AND promotion_id=1 AND performed_at IS NULL`, count)
	if err != nil {
		return adminError(err)
	}
	if result.RowsAffected() == 0 {
		return conflict("сначала настройте финальный розыгрыш; после проведения резерв менять нельзя")
	}
	return nil
}
