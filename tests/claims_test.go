//go:build integration

package tests_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	awardentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
	adminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
	awardrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/awards"
	notificationrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/notifications"
	promotionrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
	claimservice "github.com/letsssgooo/giveaways_bot/internal/services/draw/claims"
	claimadmin "github.com/letsssgooo/giveaways_bot/internal/services/draw/claims/admin"
	awardmessages "github.com/letsssgooo/giveaways_bot/internal/services/notifications/awards"
	adminservice "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/tests/utils"
)

var claimStart = time.Date(2030, 1, 20, 9, 0, 0, 0, time.UTC)

func claimSetup(t testing.TB, participants, places int, startsOn, policy string, longNotice bool) (*pgxpool.Pool, []awardentity.Award) {
	t.Helper()
	db := utils.FreezeClock(t, utils.NewDB(t), claimStart)
	drawSeed(t, db, participants, places)
	utils.Exec(t, db, `UPDATE administration_settings SET winner_response_hours=2,response_starts_on=$1,undeliverable_policy=$2 WHERE id=1`, startsOn, policy)
	if longNotice {
		utils.Exec(t, db, `UPDATE prizes SET instructions=$1 WHERE draw_id=1`, strings.Repeat("Инструкция. ", 500))
	}
	drawMustExecute(t, db, 1)
	if _, err := drawApprove(t.Context(), db, 99001, 1); err != nil {
		t.Fatal(err)
	}
	claimDrain(t, db)
	rows, err := db.Query(t.Context(), `SELECT award.id FROM prize_awards award WHERE draw_id=1 ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var awards []awardentity.Award
	for _, id := range ids {
		awards = append(awards, *claimView(t, db, id))
	}
	if len(awards) != places {
		t.Fatalf("awards=%d, want %d", len(awards), places)
	}
	return db, awards
}

func claimDrain(t testing.TB, db *pgxpool.Pool) {
	t.Helper()
	processor := awardrepo.NewAwardProcessor(db, awardmessages.AwardMessages)
	for range 100 {
		done, err := processor.ProcessNext(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !done {
			return
		}
	}
	t.Fatal("award processor did not settle")
}

func claimView(t testing.TB, db *pgxpool.Pool, id int64) *awardentity.Award {
	t.Helper()
	var result *awardentity.Award
	err := postgres.InTx(t.Context(), db, func(tx pgx.Tx) (err error) {
		result, err = awardrepo.NewAwards(tx).ViewAdmin(t.Context(), id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatalf("missing award %d", id)
	}
	return result
}

func claimAccept(ctx context.Context, db *pgxpool.Pool, awardID, userID int64) (*awardentity.Award, error) {
	var result *awardentity.Award
	err := postgres.InTx(ctx, db, func(tx pgx.Tx) (err error) {
		result, err = claimservice.NewClaims(awardrepo.NewAwards(tx)).AcceptConsent(ctx, awardID, userID)
		return err
	})
	return result, err
}

func claimConfirm(ctx context.Context, db *pgxpool.Pool, awardID, userID int64, first, last string) (*awardentity.Award, error) {
	var result *awardentity.Award
	err := postgres.InTx(ctx, db, func(tx pgx.Tx) (err error) {
		result, err = claimservice.NewClaims(awardrepo.NewAwards(tx)).Confirm(ctx, awardID, userID, first, last)
		return err
	})
	return result, err
}

func claimReplace(ctx context.Context, db *pgxpool.Pool, actor, awardID int64) error {
	return postgres.InTx(ctx, db, func(tx pgx.Tx) error {
		promotion := promotionrepo.New(tx)
		access := adminservice.NewAdmin(promotion, []int64{99001})
		return claimadmin.NewClaimAdministration(awardrepo.NewAwards(tx), access, promotion).Replace(ctx, actor, awardID)
	})
}

func claimReplacement(t testing.TB, db *pgxpool.Pool, previous int64) *awardentity.Award {
	t.Helper()
	var id int64
	if err := db.QueryRow(t.Context(), `SELECT id FROM prize_awards WHERE previous_award_id=$1`, previous).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return claimView(t, db, id)
}

func TestClaimOwnershipExplicitConsentAndImmutableConfirmation(t *testing.T) {
	t.Parallel()
	db, awards := claimSetup(t, 2, 1, "delivered", "manual", false)
	award := awards[0]
	outsider := int64(drawScalar(t, db, `SELECT id FROM users WHERE telegram_user_id=99002`))
	err := postgres.InTx(t.Context(), db, func(tx pgx.Tx) error {
		got, err := claimservice.NewClaims(awardrepo.NewAwards(tx)).View(t.Context(), award.ID, outsider)
		if got != nil {
			return fmt.Errorf("outsider read prize: %+v", got)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimAccept(t.Context(), db, award.ID, outsider); !errors.Is(err, awardentity.ErrAwardUnavailable) {
		t.Fatalf("outsider consent=%v", err)
	}
	if _, err := claimConfirm(t.Context(), db, award.ID, outsider, "Чужой", "Участник"); !errors.Is(err, awardentity.ErrAwardUnavailable) {
		t.Fatalf("outsider confirmation=%v", err)
	}
	if _, err := claimConfirm(t.Context(), db, award.ID, award.UserID, "Иван", "Иванов"); !errors.Is(err, awardentity.ErrConsentRequired) {
		t.Fatalf("confirmation without consent=%v", err)
	}
	before := claimView(t, db, award.ID)
	if before.ConsentGivenAt != nil || before.ConfirmedAt != nil || before.FirstName != "" || before.LastName != "" {
		t.Fatalf("failed claim persisted data: %+v", before)
	}
	// New campaign wording must not change the version shown for this award.
	utils.Exec(t, db, `INSERT INTO consent_versions(version,consent_text) VALUES ('v2','Consent version two');
		UPDATE promotions SET consent_version='v2',consent_text='Consent version two' WHERE id=1`)
	accepted, err := claimAccept(t.Context(), db, award.ID, award.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ConsentGivenAt == nil || accepted.ConsentVersion != "v1" || accepted.ConsentText != "Consent version one" || accepted.FirstName != "" || accepted.LastName != "" || accepted.ConfirmedAt != nil {
		t.Fatalf("consent snapshot/draft state=%+v", accepted)
	}
	utils.SetTime(t, db, claimStart.Add(time.Minute))
	repeated, err := claimAccept(t.Context(), db, award.ID, award.UserID)
	if err != nil || repeated.ConsentGivenAt == nil || !repeated.ConsentGivenAt.Equal(*accepted.ConsentGivenAt) {
		t.Fatalf("consent replay rewrote timestamp: %+v, %v", repeated, err)
	}
	confirmed, err := claimConfirm(t.Context(), db, award.ID, award.UserID, " Иван ", " Иванов ")
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.State != awardentity.AwardConfirmed || confirmed.ConfirmedAt == nil || confirmed.FirstName != "Иван" || confirmed.LastName != "Иванов" {
		t.Fatalf("confirmation=%+v", confirmed)
	}
	utils.SetTime(t, db, claimStart.Add(2*time.Minute))
	replayed, err := claimConfirm(t.Context(), db, award.ID, award.UserID, "Пётр", "Петров")
	if err != nil || replayed.FirstName != "Иван" || replayed.LastName != "Иванов" || !replayed.ConfirmedAt.Equal(*confirmed.ConfirmedAt) {
		t.Fatalf("confirmation replay changed data: %+v, err=%v", replayed, err)
	}
	if n := drawScalar(t, db, `SELECT count(*) FROM prize_awards WHERE id=$1 AND consent_version='v1' AND claim_consent_version='v1' AND claim_consent_text='Consent version one'`, award.ID); n != 1 {
		t.Fatal("confirmed consent version differs from the displayed archived version")
	}
}

func TestClaimDeadlineOriginsExactBoundaryAndStaleButton(t *testing.T) {
	for _, startsOn := range []string{"delivered", "approved"} {
		t.Run(startsOn, func(t *testing.T) {
			t.Parallel()
			db, awards := claimSetup(t, 2, 1, startsOn, "manual", true)
			award := awards[0]
			if startsOn == "delivered" && award.Deadline != nil {
				t.Fatalf("deadline started before delivery: %+v", award)
			}
			if startsOn == "approved" && (award.Deadline == nil || !award.Deadline.Equal(claimStart.Add(2*time.Hour))) {
				t.Fatalf("approval deadline=%v", award.Deadline)
			}
			if parts := drawScalar(t, db, `SELECT count(*) FROM outbox_messages WHERE prize_award_id=$1`, award.ID); parts < 2 {
				t.Fatalf("fixture must split the notification, parts=%d", parts)
			}
			firstDelivery := claimStart.Add(30 * time.Minute)
			utils.SetTime(t, db, firstDelivery)
			utils.Exec(t, db, `UPDATE outbox_messages SET status='sent',sent_at=$2 WHERE id=(SELECT min(id) FROM outbox_messages WHERE prize_award_id=$1)`, award.ID, firstDelivery)
			claimDrain(t, db)
			partial := claimView(t, db, award.ID)
			if partial.NotifiedAt != nil || (startsOn == "delivered" && partial.Deadline != nil) {
				t.Fatalf("partial delivery started deadline: %+v", partial)
			}
			lastDelivery := claimStart.Add(40 * time.Minute)
			utils.SetTime(t, db, lastDelivery)
			utils.Exec(t, db, `UPDATE outbox_messages SET status='sent',sent_at=$2 WHERE prize_award_id=$1 AND status<>'sent'`, award.ID, lastDelivery)
			// User action must refresh delivery even before the background processor.
			refreshed, err := claimAccept(t.Context(), db, award.ID, award.UserID)
			if err != nil {
				t.Fatal(err)
			}
			deadline := claimStart.Add(2 * time.Hour)
			if startsOn == "delivered" {
				deadline = lastDelivery.Add(2 * time.Hour)
			}
			if refreshed.Deadline == nil || !refreshed.Deadline.Equal(deadline) || refreshed.NotifiedAt == nil || !refreshed.NotifiedAt.Equal(lastDelivery) {
				t.Fatalf("deadline must use %s origin, got %+v", startsOn, refreshed)
			}
			utils.SetTime(t, db, deadline.Add(-time.Microsecond))
			if _, err := claimAccept(t.Context(), db, award.ID, award.UserID); err != nil {
				t.Fatalf("claim rejected before boundary: %v", err)
			}
			utils.SetTime(t, db, deadline)
			if _, err := claimConfirm(t.Context(), db, award.ID, award.UserID, "Иван", "Иванов"); !errors.Is(err, awardentity.ErrClaimExpired) {
				t.Fatalf("claim at exact deadline=%v", err)
			}
			claimDrain(t, db)
			old := claimView(t, db, award.ID)
			if old.Current || old.State != awardentity.AwardReplaced || old.ConfirmedAt != nil {
				t.Fatalf("expired award=%+v", old)
			}
			replacement := claimReplacement(t, db, award.ID)
			if !replacement.Current || replacement.UserID == award.UserID || replacement.PlaceNumber != award.PlaceNumber || replacement.ConsentVersion != "v1" {
				t.Fatalf("replacement=%+v", replacement)
			}
			if startsOn == "approved" && (replacement.Deadline == nil || !replacement.Deadline.Equal(deadline.Add(2*time.Hour))) {
				t.Fatalf("reserve must get its own deadline: %+v", replacement)
			}
			if startsOn == "delivered" && replacement.Deadline != nil {
				t.Fatalf("reserve deadline before delivery: %+v", replacement)
			}
			if _, err := claimAccept(t.Context(), db, award.ID, award.UserID); !errors.Is(err, awardentity.ErrAwardUnavailable) {
				t.Fatalf("stale claim button=%v", err)
			}
			if _, err := claimConfirm(t.Context(), db, award.ID, award.UserID, "Иван", "Иванов"); !errors.Is(err, awardentity.ErrAwardUnavailable) {
				t.Fatalf("stale confirm button=%v", err)
			}
		})
	}
}

func TestClaimUndeliverablePoliciesAndReserveExhaustion(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		participants int
		state        string
	}{
		{"manual_decision", "manual", 2, awardentity.AwardManual},
		{"automatic_reserve", "reserve", 2, awardentity.AwardReplaced},
		{"no_reserve", "reserve", 1, awardentity.AwardExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, awards := claimSetup(t, tc.participants, 1, "approved", tc.policy, false)
			award := awards[0]
			utils.Exec(t, db, `UPDATE outbox_messages SET status='failed' WHERE prize_award_id=$1`, award.ID)
			if tc.policy == "reserve" {
				if _, err := claimAccept(t.Context(), db, award.ID, award.UserID); !errors.Is(err, awardentity.ErrAwardUnavailable) {
					t.Fatalf("undeliverable recipient can still claim: %v", err)
				}
			}
			claimDrain(t, db)
			got := claimView(t, db, award.ID)
			if got.State != tc.state {
				t.Fatalf("undeliverable state=%s, want %s", got.State, tc.state)
			}
			if tc.policy == "manual" {
				if !got.Current || got.Deadline != nil {
					t.Fatalf("manual decision must freeze deadline: %+v", got)
				}
				if err := claimReplace(t.Context(), db, 99002, award.ID); !errors.Is(err, adminentity.ErrForbidden) {
					t.Fatalf("outsider replaced prize: %v", err)
				}
				if err := claimReplace(t.Context(), db, 99001, award.ID); err != nil {
					t.Fatal(err)
				}
				claimDrain(t, db)
			}
			if tc.participants == 2 {
				reserve := claimReplacement(t, db, award.ID)
				if reserve.UserID == award.UserID || !reserve.Current {
					t.Fatalf("reserve=%+v", reserve)
				}
				// When the last reserve times out there must be no invented recipient.
				if reserve.Deadline == nil {
					t.Fatal("missing reserve deadline")
				}
				utils.SetTime(t, db, *reserve.Deadline)
				claimDrain(t, db)
				last := claimView(t, db, reserve.ID)
				if last.Current || last.State != awardentity.AwardExhausted {
					t.Fatalf("exhausted reserve=%+v", last)
				}
			}
			if n := drawScalar(t, db, `SELECT count(*) FROM prize_awards WHERE draw_id=1 AND is_current`); n != 0 {
				t.Fatalf("current awards after exhaustion=%d", n)
			}
			if n := drawScalar(t, db, `SELECT count(*) FROM prize_awards WHERE draw_id=1`); n != tc.participants {
				t.Fatalf("award chain=%d, want %d", n, tc.participants)
			}
		})
	}
}

func TestClaimConcurrentReplacementUsesDifferentReserves(t *testing.T) {
	t.Parallel()
	db, awards := claimSetup(t, 4, 2, "approved", "manual", false)
	const oldToken = "12345678-1234-1234-1234-123456789012"
	utils.Exec(t, db, `UPDATE outbox_messages SET status='processing',lease_until=$1,attempt_token=$2::uuid
		WHERE prize_award_id IS NOT NULL`, claimStart.Add(24*time.Hour), oldToken)
	oldMessageID := int64(drawScalar(t, db, `SELECT min(id) FROM outbox_messages WHERE prize_award_id=$1`, awards[0].ID))
	utils.SetTime(t, db, claimStart.Add(2*time.Hour))
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			processor := awardrepo.NewAwardProcessor(db, awardmessages.AwardMessages)
			if _, err := processor.ProcessNext(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	claimDrain(t, db)
	first, second := claimReplacement(t, db, awards[0].ID), claimReplacement(t, db, awards[1].ID)
	if first.UserID == second.UserID || !first.Current || !second.Current {
		t.Fatalf("reserve recipients=%+v, %+v", first, second)
	}
	if n := drawScalar(t, db, `SELECT count(DISTINCT participant.user_id) FROM prize_awards award
		JOIN draw_results result ON result.id=award.draw_result_id
		JOIN draw_participants participant ON participant.id=result.draw_participant_id WHERE award.draw_id=1`); n != 4 {
		t.Fatalf("distinct original/reserve recipients=%d", n)
	}
	if n := drawScalar(t, db, `SELECT count(*) FROM outbox_messages WHERE prize_award_id IN ($1,$2) AND (status<>'failed' OR lease_until IS NOT NULL OR attempt_token IS NOT NULL)`, awards[0].ID, awards[1].ID); n != 0 {
		t.Fatalf("obsolete notifications still deliverable=%d", n)
	}
	if err := notificationrepo.NewQueue(db).Sent(t.Context(), oldMessageID, oldToken); !errors.Is(err, notificationrepo.ErrLeaseLost) {
		t.Fatalf("old sender acknowledged delivery after replacement: %v", err)
	}
}

func TestClaimConfirmationRacesWithReplacement(t *testing.T) {
	t.Parallel()
	db, awards := claimSetup(t, 2, 1, "delivered", "manual", false)
	award := awards[0]
	if _, err := claimAccept(t.Context(), db, award.ID, award.UserID); err != nil {
		t.Fatal(err)
	}
	utils.Exec(t, db, `UPDATE outbox_messages SET status='failed' WHERE prize_award_id=$1`, award.ID)
	claimDrain(t, db)
	if claimView(t, db, award.ID).State != awardentity.AwardManual {
		t.Fatal("fixture did not reach manual decision")
	}
	// Hold the shared draw lock while both operations start, then let the actual
	// repositories serialize them. Either order is valid; both succeeding is not.
	blocker, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	utils.Exec(t, blocker, `SELECT id FROM draws WHERE id=1 FOR UPDATE`)
	var blockerPID int
	if err := blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		action string
		err    error
	}
	results := make(chan outcome, 2)
	ready := make(chan struct{}, 2)
	go func() {
		ready <- struct{}{}
		_, err := claimConfirm(t.Context(), db, award.ID, award.UserID, "Иван", "Иванов")
		results <- outcome{"confirm", err}
	}()
	go func() {
		ready <- struct{}{}
		results <- outcome{"replace", claimReplace(t.Context(), db, 99001, award.ID)}
	}()
	<-ready
	<-ready
	claimWaitForBlockedOperations(t, db, blockerPID, 2)
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	winner := ""
	for range 2 {
		got := <-results
		if got.err == nil {
			if winner != "" {
				t.Fatal("both confirmation and replacement succeeded")
			}
			winner = got.action
		} else if !errors.Is(got.err, awardentity.ErrAwardUnavailable) {
			t.Fatalf("%s error=%v", got.action, got.err)
		}
	}
	if winner == "" {
		t.Fatal("neither operation succeeded")
	}
	if winner == "replace" {
		claimDrain(t, db)
		reserve := claimReplacement(t, db, award.ID)
		if _, err := claimConfirm(t.Context(), db, award.ID, award.UserID, "Иван", "Иванов"); !errors.Is(err, awardentity.ErrAwardUnavailable) {
			t.Fatalf("stale confirmation after race=%v", err)
		}
		if _, err := claimAccept(t.Context(), db, reserve.ID, reserve.UserID); err != nil {
			t.Fatal(err)
		}
		if _, err := claimConfirm(t.Context(), db, reserve.ID, reserve.UserID, "Пётр", "Петров"); err != nil {
			t.Fatal(err)
		}
	}
	if n := drawScalar(t, db, `SELECT count(*) FROM prize_awards WHERE draw_id=1 AND confirmed_at IS NOT NULL`); n != 1 {
		t.Fatalf("confirmed recipients=%d, want 1", n)
	}
	if n := drawScalar(t, db, `SELECT count(*) FROM prize_awards WHERE draw_id=1 AND is_current`); n != 1 {
		t.Fatalf("current recipients=%d, want 1", n)
	}
}

func claimWaitForBlockedOperations(t testing.TB, db *pgxpool.Pool, blockerPID, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		// The second operation can wait behind the first waiter's tuple lock.
		// Follow the lock chain, restricting the result to this schema's draw.
		err := db.QueryRow(ctx, `WITH RECURSIVE blocked(pid) AS (
			SELECT pid FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid))
			UNION SELECT activity.pid FROM pg_stat_activity activity JOIN blocked
			ON blocked.pid=ANY(pg_blocking_pids(activity.pid)))
			SELECT count(DISTINCT blocked.pid) FROM blocked JOIN pg_locks USING(pid)
			WHERE pg_locks.relation='draws'::regclass`, blockerPID).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("blocked operations=%d, want %d: %v", count, want, ctx.Err())
		case <-ticker.C:
		}
	}
}
