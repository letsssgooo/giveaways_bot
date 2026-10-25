package exports

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/reporting"
)

type Repository struct{ tx pgx.Tx }

func New(tx pgx.Tx) *Repository { return &Repository{tx: tx} }

func (r *Repository) Participants(ctx context.Context, scope entity.Scope) ([]entity.Participant, error) {
	rows, err := r.tx.Query(ctx, `WITH participants AS (
		SELECT users.telegram_user_id,partner.id AS partner_id,day.id AS partner_day_id,
			partner.name AS partner_name,day.date,registration.registered_at,
			coalesce(draw.id,0) AS draw_id,'daily'::text AS draw_kind,1 AS weight
		FROM registrations AS registration
		JOIN users ON users.id=registration.user_id
		JOIN partner_days AS day ON day.id=registration.partner_day_id
		JOIN partners AS partner ON partner.id=day.partner_id
		LEFT JOIN draws AS draw ON draw.partner_day_id=day.id AND draw.kind='daily'
		WHERE $1 OR partner.id=ANY($2::bigint[])
		UNION ALL
		SELECT users.telegram_user_id,0,0,'',promotion.ends_on,NULL::timestamptz,draw.id,'final',participant.weight
		FROM draw_participants AS participant JOIN draws AS draw ON draw.id=participant.draw_id
		JOIN users ON users.id=participant.user_id
		JOIN promotions AS promotion ON promotion.id=draw.promotion_id
		WHERE $1 AND $4 AND draw.kind='final' AND draw.performed_at IS NOT NULL
	)
	SELECT telegram_user_id,partner_id,partner_day_id,partner_name,date,registered_at,draw_id,draw_kind,weight
	FROM participants ORDER BY date,draw_kind,partner_id,partner_day_id,telegram_user_id LIMIT $3`,
		scope.AllPartners, scope.PartnerIDs, entity.MaxExportRows+1, scope.IncludeUsers)
	if err != nil {
		return nil, fmt.Errorf("read participants export: %w", err)
	}
	defer rows.Close()
	participants := make([]entity.Participant, 0)
	for rows.Next() {
		if len(participants) == entity.MaxExportRows {
			return nil, entity.ErrExportTooLarge
		}
		var participant entity.Participant
		if err := rows.Scan(&participant.TelegramID, &participant.PartnerID, &participant.PartnerDayID,
			&participant.PartnerName, &participant.Date, &participant.RegisteredAt,
			&participant.DrawID, &participant.DrawKind, &participant.Weight); err != nil {
			return nil, fmt.Errorf("scan participants export: %w", err)
		}
		participants = append(participants, participant)
	}
	return participants, rows.Err()
}

func (r *Repository) Winners(ctx context.Context, scope entity.Scope) ([]entity.Winner, error) {
	// draw_result_id уникален у назначения: победитель, исходный резервист и
	// заменивший его получатель представлены каждый своей единственной строкой.
	// Согласие сверяется с сохраненной версией и текстом, а не текущим материалом.
	rows, err := r.tx.Query(ctx, `SELECT draw.id,coalesce(day.partner_id,0),coalesce(day.id,0),users.telegram_user_id,
		coalesce(prize.id,0),coalesce(award.id,0),draw.kind,coalesce(partner.name,''),result.role,
		coalesce(prize.name,''),coalesce(award.claim_state,CASE WHEN result.role='reserve' THEN 'reserve' ELSE 'unassigned' END),
		CASE WHEN consent.valid THEN award.consent_version ELSE '' END,
		CASE WHEN consent.valid THEN coalesce(award.first_name,'') ELSE '' END,
		CASE WHEN consent.valid THEN coalesce(award.last_name,'') ELSE '' END,
		coalesce(day.date,promotion.ends_on),registration.registered_at,draw.performed_at,draw.approved_at,
		award.confirmed_at,CASE WHEN consent.valid THEN award.consent_given_at END,
		participant.weight,result.position,coalesce(award.place_number,result.place_number,0),
		coalesce(prize.units_per_winner,0),coalesce(award.is_current,false)
		FROM draw_results AS result
		JOIN draws AS draw ON draw.id=result.draw_id
		JOIN promotions AS promotion ON promotion.id=draw.promotion_id
		JOIN draw_participants AS participant ON participant.id=result.draw_participant_id
		JOIN users ON users.id=participant.user_id
		LEFT JOIN registrations AS registration ON registration.id=participant.registration_id
		LEFT JOIN partner_days AS day ON day.id=draw.partner_day_id
		LEFT JOIN partners AS partner ON partner.id=day.partner_id
		LEFT JOIN prize_awards AS award ON award.draw_result_id=result.id
		LEFT JOIN prizes AS prize ON prize.id=coalesce(award.prize_id,result.prize_id)
		LEFT JOIN consent_versions AS version ON version.version=award.consent_version
		CROSS JOIN LATERAL(SELECT award.consent_given_at IS NOT NULL
			AND award.consent_version=award.claim_consent_version
			AND version.consent_text=award.claim_consent_text
			AND btrim(award.claim_consent_text)<>'' AS valid) AS consent
		WHERE draw.performed_at IS NOT NULL AND ($1 OR day.partner_id=ANY($2::bigint[]))
		ORDER BY draw.scheduled_at,draw.id,CASE result.role WHEN 'winner' THEN 0 ELSE 1 END,result.position
		LIMIT $3`, scope.AllPartners, scope.PartnerIDs, entity.MaxExportRows+1)
	if err != nil {
		return nil, fmt.Errorf("read winners export: %w", err)
	}
	defer rows.Close()
	winners := make([]entity.Winner, 0)
	for rows.Next() {
		if len(winners) == entity.MaxExportRows {
			return nil, entity.ErrExportTooLarge
		}
		var winner entity.Winner
		if err := rows.Scan(&winner.DrawID, &winner.PartnerID, &winner.PartnerDayID, &winner.TelegramID,
			&winner.PrizeID, &winner.AwardID, &winner.DrawKind, &winner.PartnerName, &winner.Role,
			&winner.PrizeName, &winner.AwardStatus, &winner.ConsentVersion, &winner.FirstName, &winner.LastName,
			&winner.Date, &winner.RegisteredAt, &winner.PerformedAt, &winner.ApprovedAt, &winner.ConfirmedAt,
			&winner.ConsentGivenAt, &winner.Weight, &winner.Position, &winner.PlaceNumber, &winner.Units, &winner.AwardCurrent); err != nil {
			return nil, fmt.Errorf("scan winners export: %w", err)
		}
		winners = append(winners, winner)
	}
	return winners, rows.Err()
}
