package importer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type Repository struct{ tx pgx.Tx }

func New(tx pgx.Tx) *Repository { return &Repository{tx: tx} }

// AddCodes не обновляет существующие коды: повторная загрузка не меняет выдачу.
func (r *Repository) AddCodes(ctx context.Context, partnerID int64, codes []string) (int, error) {
	var lockedPartner int64
	if err := r.tx.QueryRow(ctx, `SELECT id FROM partners WHERE id = $1 FOR KEY SHARE`, partnerID).Scan(&lockedPartner); err != nil {
		return 0, fmt.Errorf("lock import partner: %w", err)
	}
	result, err := r.tx.Exec(ctx, `INSERT INTO promo_codes(partner_id, code)
		SELECT $1, code FROM unnest($2::text[]) AS code
		ON CONFLICT (partner_id, code) DO NOTHING`, partnerID, codes)
	if err != nil {
		return 0, fmt.Errorf("add promo codes: %w", err)
	}
	return int(result.RowsAffected()), nil
}
