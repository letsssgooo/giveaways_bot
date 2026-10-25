//go:build integration

package tests_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	drawentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
	adminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
	drawrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw"
	awardrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/awards"
	promotionrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
	drawservice "github.com/letsssgooo/giveaways_bot/internal/services/draw"
	awardmessages "github.com/letsssgooo/giveaways_bot/internal/services/notifications/awards"
	adminservice "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	"github.com/letsssgooo/giveaways_bot/tests/utils"
)

// Historical fixture dates keep scheduling assertions independent of the host's
// date. The fixture contains two partners on the same day and separate admins.
func drawSeed(t testing.TB, db *pgxpool.Pool, participants, dailyPlaces int) {
	t.Helper()
	utils.Exec(t, db, `
		INSERT INTO promotions(name,starts_on,ends_on,consent_version,consent_text)
		VALUES ('Integration giveaway','2020-01-01','2020-01-15','v1','Consent version one');
		INSERT INTO consent_versions(version,consent_text) VALUES ('v1','Consent version one');
		INSERT INTO partners(name,telegram_chat_id,subscription_url)
		VALUES ('First',-1001,'https://t.me/first'),('Second',-1002,'https://t.me/second');
		INSERT INTO partner_days(partner_id,date,opens_at,closes_at) VALUES
		(1,'2020-01-01','2020-01-01 09:00:00+03','2020-01-01 21:00:00+03'),
		(2,'2020-01-01','2020-01-01 09:00:00+03','2020-01-01 21:00:00+03');
		INSERT INTO draws(kind,partner_day_id,scheduled_at) VALUES ('daily',1,'2020-01-01 22:00:00+03');
		INSERT INTO draws(kind,scheduled_at) VALUES ('final','2020-01-16 00:00:00+03');
	`)
	utils.Exec(t, db, `INSERT INTO users(telegram_user_id,chat_id) SELECT 1000+n,1000+n FROM generate_series(1,$1::int) n`, participants)
	utils.Exec(t, db, `INSERT INTO users(telegram_user_id,chat_id) VALUES (99001,99001),(99002,99002),(99003,99003)`)
	utils.Exec(t, db, `INSERT INTO registrations(user_id,partner_day_id,registered_at,subscription_checked_at)
		SELECT id,1,'2020-01-01 10:00:00+03','2020-01-01 09:59:00+03' FROM users WHERE telegram_user_id<99000`)
	utils.Exec(t, db, `INSERT INTO prizes(draw_id,name,instructions,places_count,units_per_winner)
		VALUES (1,'Daily gift','Collect the gift',$1,1),(2,'Final tickets','Collect the tickets',5,2)`, dailyPlaces)
}

func drawExecute(ctx context.Context, db *pgxpool.Pool, id int64) (*drawentity.Result, bool, error) {
	var result *drawentity.Result
	var performed bool
	err := postgres.InTx(ctx, db, func(tx pgx.Tx) (err error) {
		result, performed, err = drawrepo.New(tx).ExecuteWithStatus(ctx, id)
		return err
	})
	return result, performed, err
}

func drawMustExecute(t testing.TB, db *pgxpool.Pool, id int64) *drawentity.Result {
	t.Helper()
	result, performed, err := drawExecute(t.Context(), db, id)
	if err != nil || !performed {
		t.Fatalf("execute draw %d: performed=%v, err=%v", id, performed, err)
	}
	return result
}

func drawApprove(ctx context.Context, db *pgxpool.Pool, actor, id int64) (*drawentity.Result, error) {
	var result *drawentity.Result
	err := postgres.InTx(ctx, db, func(tx pgx.Tx) (err error) {
		access := adminservice.NewAdmin(promotionrepo.New(tx), []int64{99001})
		result, err = drawservice.New(drawrepo.New(tx), access).Approve(ctx, actor, id)
		return err
	})
	return result, err
}

func drawScalar(t testing.TB, db *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(t.Context(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func drawAssertDistinct(t testing.TB, result *drawentity.Result) {
	t.Helper()
	seen := map[int64]bool{}
	for _, list := range [][]drawentity.Selection{result.Winners, result.Reserves} {
		for i, person := range list {
			if seen[person.UserID] {
				t.Fatalf("user %d selected more than once: %+v", person.UserID, result)
			}
			seen[person.UserID] = true
			if person.Position != i+1 {
				t.Fatalf("position=%d, want %d", person.Position, i+1)
			}
		}
	}
}

func TestDrawDailyEligibilityAndPrizePlaces(t *testing.T) {
	t.Parallel()
	db := utils.NewDB(t)
	drawSeed(t, db, 8, 2)
	utils.Exec(t, db, `INSERT INTO prizes(draw_id,name,instructions,places_count,units_per_winner) VALUES (1,'Bonus gift','Instructions',1,3)`)
	// A registration with the other partner on the same date is not eligible.
	utils.Exec(t, db, `INSERT INTO registrations(user_id,partner_day_id,registered_at,subscription_checked_at)
		SELECT id,2,'2020-01-01 10:00:00+03','2020-01-01 09:59:00+03' FROM users WHERE telegram_user_id=99002`)
	result := drawMustExecute(t, db, 1)
	if result.ParticipantCount != 8 || len(result.Participants) != 8 || len(result.Winners) != 3 || len(result.Reserves) != 3 {
		t.Fatalf("unexpected daily result: %+v", result)
	}
	for _, participant := range result.Participants {
		if participant.TelegramID < 1001 || participant.TelegramID > 1008 || participant.Weight != 1 {
			t.Fatalf("unexpected daily participant: %+v", participant)
		}
	}
	drawAssertDistinct(t, result)
	wantPlaces := []struct {
		prize        int64
		place, units int
	}{{1, 1, 1}, {1, 2, 1}, {3, 1, 3}}
	for i, winner := range result.Winners {
		want := wantPlaces[i]
		if winner.PrizeID != want.prize || winner.PlaceNumber != want.place || winner.UnitsPerWinner != want.units {
			t.Fatalf("winner slot %d = %+v, want %+v", i, winner, want)
		}
	}
	if got := drawScalar(t, db, `SELECT count(*) FROM prize_awards WHERE draw_id=1 AND is_current`); got != 3 {
		t.Fatalf("current daily awards=%d, want 3", got)
	}
}

func TestDrawFinalWeightsAndPreviousDailyWinners(t *testing.T) {
	t.Parallel()
	db := utils.NewDB(t)
	drawSeed(t, db, 11, 2)
	utils.Exec(t, db, `INSERT INTO active_days(user_id,date,activated_at)
		SELECT users.id,'2020-01-01'::date+n,('2020-01-01'::date+n)::timestamp AT TIME ZONE 'Europe/Moscow'
		FROM users CROSS JOIN LATERAL generate_series(0,CASE WHEN users.id=1 THEN 11 ELSE least(users.id::int,10)-1 END) n
		WHERE telegram_user_id<99000`)
	// Activity outside the campaign must neither add weight nor admit a user.
	utils.Exec(t, db, `INSERT INTO active_days(user_id,date,activated_at)
		SELECT id,date,date::timestamp AT TIME ZONE 'Europe/Moscow' FROM users
		CROSS JOIN (VALUES ('2019-12-31'::date),('2020-01-16'::date)) days(date)
		WHERE telegram_user_id IN (1002,99002)`)
	daily := drawMustExecute(t, db, 1)
	final := drawMustExecute(t, db, 2)
	if len(final.Participants) != 11 || len(final.Winners) != 5 || len(final.Reserves) != 5 {
		t.Fatalf("unexpected final result: %+v", final)
	}
	eligible := map[int64]bool{}
	for _, participant := range final.Participants {
		wantWeight := min(int(participant.TelegramID-1000), 10)
		if participant.TelegramID == 1001 {
			wantWeight = 10
		}
		if participant.TelegramID >= 99000 || participant.Weight != wantWeight {
			t.Fatalf("final participant=%+v, expected weight=%d", participant, wantWeight)
		}
		eligible[participant.UserID] = true
	}
	for _, winner := range daily.Winners {
		if !eligible[winner.UserID] {
			t.Fatalf("daily winner %d was excluded from final", winner.UserID)
		}
	}
	drawAssertDistinct(t, final)
	for i, winner := range final.Winners {
		if winner.UnitsPerWinner != 2 || winner.PlaceNumber != i+1 {
			t.Fatalf("final prize slot: %+v", winner)
		}
	}
	if got := drawScalar(t, db, `SELECT count(*) FROM active_days WHERE user_id=1`); got != 12 {
		t.Fatalf("history above the ten-chance cap was changed: %d", got)
	}
}

func TestDrawEmptyAndInsufficientParticipants(t *testing.T) {
	for _, kind := range []string{"daily", "final"} {
		for _, count := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/%d", kind, count), func(t *testing.T) {
				t.Parallel()
				db := utils.NewDB(t)
				drawSeed(t, db, count, 3)
				utils.Exec(t, db, `INSERT INTO active_days(user_id,date,activated_at)
					SELECT id,'2020-01-01','2020-01-01 10:00:00+03' FROM users WHERE telegram_user_id<99000`)
				id := int64(1)
				if kind == "final" {
					id = 2
				}
				first := drawMustExecute(t, db, id)
				if first.PerformedAt == nil || len(first.Participants) != count || len(first.Winners) != count || len(first.Reserves) != 0 {
					t.Fatalf("unexpected result with %d participants: %+v", count, first)
				}
				second, performed, err := drawExecute(t.Context(), db, id)
				if err != nil || performed || !reflect.DeepEqual(first, second) {
					t.Fatalf("empty/short draw changed on replay: performed=%v, err=%v", performed, err)
				}
			})
		}
	}
}

func TestDrawRollbackAndConcurrentExecution(t *testing.T) {
	for _, id := range []int64{1, 2} {
		t.Run(fmt.Sprintf("draw_%d", id), func(t *testing.T) {
			t.Parallel()
			db := utils.NewDB(t)
			drawSeed(t, db, 10, 3)
			utils.Exec(t, db, `INSERT INTO active_days(user_id,date,activated_at)
				SELECT id,'2020-01-01','2020-01-01 10:00:00+03' FROM users WHERE telegram_user_id<99000`)
			// Fail after the snapshot and first award have already been inserted.
			utils.Exec(t, db, `CREATE FUNCTION integration_fail_second_winner() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN IF NEW.role='winner' AND NEW.position=2 THEN RAISE EXCEPTION 'integration injected draw failure'; END IF; RETURN NEW; END $$;
				CREATE TRIGGER integration_fail_second_winner BEFORE INSERT ON draw_results
				FOR EACH ROW EXECUTE FUNCTION integration_fail_second_winner()`)
			if _, _, err := drawExecute(t.Context(), db, id); err == nil || !strings.Contains(err.Error(), "integration injected draw failure") {
				t.Fatalf("expected the injected database failure, got %v", err)
			}
			for _, table := range []string{"draw_participants", "draw_results", "prize_awards"} {
				if n := drawScalar(t, db, `SELECT count(*) FROM `+table+` WHERE draw_id=$1`, id); n != 0 {
					t.Fatalf("partial %s survived failed draw: %d", table, n)
				}
			}
			if n := drawScalar(t, db, `SELECT count(*) FROM draws WHERE id=$1 AND performed_at IS NOT NULL`, id); n != 0 {
				t.Fatal("failed draw marked performed")
			}
			utils.Exec(t, db, `DROP TRIGGER integration_fail_second_winner ON draw_results`)
			type outcome struct {
				result    *drawentity.Result
				performed bool
				err       error
			}
			results := make(chan outcome, 8)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for range cap(results) {
				wg.Go(func() {
					<-start
					result, performed, err := drawExecute(t.Context(), db, id)
					results <- outcome{result, performed, err}
				})
			}
			close(start)
			wg.Wait()
			close(results)
			var first *drawentity.Result
			performedCount := 0
			for got := range results {
				if got.err != nil {
					t.Fatal(got.err)
				}
				if got.performed {
					performedCount++
				}
				if first == nil {
					first = got.result
				}
				if !reflect.DeepEqual(first, got.result) {
					t.Fatal("concurrent calls returned different snapshots/results")
				}
			}
			if performedCount != 1 {
				t.Fatalf("performed %d times, want 1", performedCount)
			}
			// Even changed source data cannot reroll an already performed draw.
			utils.Exec(t, db, `INSERT INTO registrations(user_id,partner_day_id,registered_at,subscription_checked_at)
				SELECT id,1,'2020-01-01 10:00:00+03','2020-01-01 09:59:00+03' FROM users WHERE telegram_user_id=99002`)
			utils.Exec(t, db, `INSERT INTO active_days(user_id,date,activated_at)
				SELECT id,'2020-01-01','2020-01-01 10:00:00+03' FROM users WHERE telegram_user_id=99002`)
			got, performed, err := drawExecute(t.Context(), db, id)
			if err != nil || performed || !reflect.DeepEqual(first, got) {
				t.Fatalf("replay changed snapshot: performed=%v err=%v", performed, err)
			}
		})
	}
}

func TestDrawApprovalAuthorizationAndSingleNotificationJob(t *testing.T) {
	t.Parallel()
	db := utils.NewDB(t)
	drawSeed(t, db, 6, 2)
	utils.Exec(t, db, `INSERT INTO active_days(user_id,date,activated_at)
		SELECT id,'2020-01-01','2020-01-01 10:00:00+03' FROM users WHERE telegram_user_id<99000`)
	if _, err := drawApprove(t.Context(), db, 99001, 1); !errors.Is(err, drawentity.ErrNotReady) {
		t.Fatalf("approval before execution=%v", err)
	}
	drawMustExecute(t, db, 1)
	drawMustExecute(t, db, 2)
	processor := awardrepo.NewAwardProcessor(db, awardmessages.AwardMessages)
	if done, err := processor.ProcessNext(t.Context()); err != nil || done {
		t.Fatalf("notices started before approval: done=%v err=%v", done, err)
	}
	if got := drawScalar(t, db, `SELECT count(*) FROM outbox_messages`); got != 0 {
		t.Fatalf("preapproval outbox=%d", got)
	}
	if got := drawScalar(t, db, `SELECT count(*) FROM background_jobs`); got != 0 {
		t.Fatalf("preapproval jobs=%d", got)
	}
	if _, err := drawApprove(t.Context(), db, 99002, 1); !errors.Is(err, adminentity.ErrForbidden) {
		t.Fatalf("non-admin approval=%v", err)
	}
	utils.Exec(t, db, `INSERT INTO administration_grants(telegram_user_id,partner_id) VALUES (99003,2)`)
	if _, err := drawApprove(t.Context(), db, 99003, 1); !errors.Is(err, adminentity.ErrForbidden) {
		t.Fatalf("foreign partner approval=%v", err)
	}
	utils.Exec(t, db, `INSERT INTO administration_grants(telegram_user_id,partner_id) VALUES (99003,1)`)
	if _, err := drawApprove(t.Context(), db, 99003, 2); !errors.Is(err, adminentity.ErrForbidden) {
		t.Fatalf("partner approved final: %v", err)
	}
	for _, id := range []int64{1, 2} {
		actor := int64(99003)
		if id == 2 {
			actor = 99001
		}
		type approvalOutcome struct {
			result *drawentity.Result
			err    error
		}
		results := make(chan approvalOutcome, 6)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range cap(results) {
			wg.Go(func() {
				<-start
				result, err := drawApprove(t.Context(), db, actor, id)
				results <- approvalOutcome{result, err}
			})
		}
		close(start)
		wg.Wait()
		close(results)
		var first *drawentity.Result
		for got := range results {
			if got.err != nil {
				t.Fatal(got.err)
			}
			if first == nil {
				first = got.result
			}
			if !reflect.DeepEqual(first, got.result) {
				t.Fatal("concurrent approvals returned different results")
			}
		}
		if first.ApprovedAt == nil || first.ApprovedBy == 0 {
			t.Fatalf("missing approval audit: %+v", first.Summary)
		}
		replayed, err := drawApprove(t.Context(), db, 99001, id)
		if err != nil {
			t.Fatal(err)
		}
		if replayed.ApprovedBy != first.ApprovedBy || !replayed.ApprovedAt.Equal(*first.ApprovedAt) {
			t.Fatalf("repeated approval rewrote audit: %+v", replayed.Summary)
		}
		if got := drawScalar(t, db, `SELECT count(*) FROM background_jobs WHERE kind='notify_draw_winners' AND payload->>'draw_id'=$1`, fmt.Sprint(id)); got != 1 {
			t.Fatalf("draw %d notification jobs=%d, want 1", id, got)
		}
	}
}
