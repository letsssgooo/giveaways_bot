//go:build integration

package tests_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/notifications"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
	broadcastrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/notifications/broadcasts"
	broadcastservice "github.com/letsssgooo/giveaways_bot/internal/services/notifications/broadcasts"
	adminservice "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/tests/utils"
)

func TestBroadcastFrozenAudienceAndConcurrentConfirmation(t *testing.T) {
	db := utils.NewDB(t)
	f := seedAdministration(t, db)
	// User 200 registered, user 201 obtained a code; both belong to this partner.
	utils.Exec(t, db, `INSERT INTO registrations(user_id,partner_day_id,registered_at,subscription_checked_at)
		SELECT id,$1,statement_timestamp(),statement_timestamp() FROM users WHERE telegram_user_id=200`, f.day)
	utils.Exec(t, db, `INSERT INTO promo_codes(partner_id,code,user_id,partner_day_id,issued_at)
		SELECT $1,'CODE-201',id,$2,clock_timestamp() FROM users WHERE telegram_user_id=201`, f.partner, f.day)
	utils.Exec(t, db, `INSERT INTO telegram_updates(update_id,payload) VALUES(1,'{}')`)
	var draft *entity.Broadcast
	adminTx(t, db, func(tx pgx.Tx) error {
		service := broadcastservice.NewBroadcasts(broadcastrepo.New(tx), adminService(tx))
		var err error
		draft, err = service.Create(t.Context(), adminPartnerActorID, 1, entity.Audience{Kind: entity.AudiencePartner, PartnerID: f.partner}, "Partner news")
		return err
	})
	if draft.Total != 2 || draft.Status != entity.BroadcastDraft {
		t.Fatalf("draft = %+v", draft)
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM outbox_messages WHERE broadcast_id=$1`, draft.ID); got != 0 {
		t.Fatalf("draft queued %d messages before confirmation", got)
	}
	// A new eligible participant after the preview must not silently expand its audience.
	utils.Exec(t, db, `INSERT INTO registrations(user_id,partner_day_id,registered_at,subscription_checked_at)
		SELECT id,$1,statement_timestamp(),statement_timestamp() FROM users WHERE telegram_user_id=202`, f.day)
	adminTx(t, db, func(tx pgx.Tx) error {
		repeated, err := broadcastservice.NewBroadcasts(broadcastrepo.New(tx), adminService(tx)).Create(t.Context(), adminPartnerActorID, 1, entity.Audience{Kind: entity.AudiencePartner, PartnerID: f.partner}, "Changed retry text")
		if err == nil && (repeated.ID != draft.ID || repeated.Total != 2 || repeated.Text != "Partner news") {
			t.Fatalf("repeated create changed snapshot: %+v", repeated)
		}
		return err
	})
	const attempts = 8
	errs := make(chan error, attempts)
	queuedTimes := make(chan time.Time, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Go(func() {
			errs <- postgres.InTx(t.Context(), db, func(tx pgx.Tx) error {
				value, err := broadcastservice.NewBroadcasts(broadcastrepo.New(tx), adminService(tx)).Send(t.Context(), adminPartnerActorID, draft.ID)
				if err == nil && value.QueuedAt != nil {
					queuedTimes <- *value.QueuedAt
				}
				return err
			})
		})
	}
	wg.Wait()
	close(errs)
	close(queuedTimes)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first time.Time
	var count int
	for queuedAt := range queuedTimes {
		count++
		if first.IsZero() {
			first = queuedAt
		} else if !queuedAt.Equal(first) {
			t.Fatal("repeated confirmation changed queued timestamp")
		}
	}
	if count != attempts {
		t.Fatalf("only %d confirmations observed queued timestamp", count)
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM outbox_messages WHERE broadcast_id=$1`, draft.ID); got != 2 {
		t.Fatalf("concurrent confirmations queued %d messages, want 2", got)
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM broadcast_recipients r JOIN users u ON u.id=r.user_id
		WHERE r.broadcast_id=$1 AND u.telegram_user_id NOT IN(200,201)`, draft.ID); got != 0 {
		t.Fatalf("snapshot contains %d unexpected recipients", got)
	}
}

func TestBroadcastAuthorizationAndInvalidAudienceAreAtomic(t *testing.T) {
	db := utils.NewDB(t)
	f := seedAdministration(t, db)
	utils.Exec(t, db, `INSERT INTO telegram_updates(update_id,payload) SELECT id,'{}' FROM generate_series(1,5) id`)
	for i, audience := range []entity.Audience{
		{Kind: entity.AudienceAll},
		{Kind: entity.AudiencePartner, PartnerID: f.otherPartner},
		{Kind: entity.AudienceUsers, TelegramIDs: []int64{200}},
	} {
		err := postgres.InTx(t.Context(), db, func(tx pgx.Tx) error {
			_, err := broadcastservice.NewBroadcasts(broadcastrepo.New(tx), adminService(tx)).Create(t.Context(), adminPartnerActorID, int64(i+1), audience, "Forbidden")
			return err
		})
		if !errors.Is(err, adminservice.ErrForbidden) {
			t.Fatalf("audience %+v: %v", audience, err)
		}
	}
	for i, audience := range []entity.Audience{
		{Kind: entity.AudiencePartner, PartnerID: f.partner}, // No participation yet.
		{Kind: entity.AudienceUsers, TelegramIDs: []int64{200, 999999}},
	} {
		err := postgres.InTx(t.Context(), db, func(tx pgx.Tx) error {
			_, err := broadcastservice.NewBroadcasts(broadcastrepo.New(tx), adminService(tx)).Create(t.Context(), adminOrganizerID, int64(i+4), audience, "Invalid audience")
			return err
		})
		if !errors.Is(err, adminservice.ErrInvalid) {
			t.Fatalf("invalid audience %+v: %v", audience, err)
		}
	}
	if got := adminScalar(t, db, `SELECT (SELECT count(*) FROM broadcasts)+(SELECT count(*) FROM broadcast_recipients)+(SELECT count(*) FROM outbox_messages)`); got != 0 {
		t.Fatalf("forbidden/invalid broadcast left %d rows", got)
	}
}

func TestBroadcastRevokedAccessCannotConfirmDraft(t *testing.T) {
	db := utils.NewDB(t)
	f := seedAdministration(t, db)
	utils.Exec(t, db, `INSERT INTO registrations(user_id,partner_day_id,registered_at,subscription_checked_at)
		SELECT id,$1,statement_timestamp(),statement_timestamp() FROM users WHERE telegram_user_id=200`, f.day)
	utils.Exec(t, db, `INSERT INTO telegram_updates(update_id,payload) VALUES(1,'{}')`)
	var draft *entity.Broadcast
	adminTx(t, db, func(tx pgx.Tx) error {
		var err error
		draft, err = broadcastservice.NewBroadcasts(broadcastrepo.New(tx), adminService(tx)).Create(t.Context(), adminPartnerActorID, 1, entity.Audience{Kind: entity.AudiencePartner, PartnerID: f.partner}, "Draft")
		return err
	})
	adminTx(t, db, func(tx pgx.Tx) error {
		return adminService(tx).Revoke(t.Context(), adminOrganizerID, adminPartnerActorID, f.partner)
	})
	err := postgres.InTx(t.Context(), db, func(tx pgx.Tx) error {
		_, err := broadcastservice.NewBroadcasts(broadcastrepo.New(tx), adminService(tx)).Send(t.Context(), adminPartnerActorID, draft.ID)
		return err
	})
	if !errors.Is(err, adminservice.ErrForbidden) {
		t.Fatalf("revoked confirmation error = %v", err)
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM broadcasts WHERE id=$1 AND status='draft' AND queued_at IS NULL`, draft.ID); got != 1 {
		t.Fatal("revoked access changed the broadcast draft")
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM outbox_messages WHERE broadcast_id=$1`, draft.ID); got != 0 {
		t.Fatalf("revoked confirmation queued %d recipients", got)
	}
}
