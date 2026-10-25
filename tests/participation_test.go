//go:build integration

package tests_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/participation"
	registrationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation/registration"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
	coderepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/participation"
	registrationrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/participation/registration"
	promotionrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
	codeservice "github.com/letsssgooo/giveaways_bot/internal/services/participation"
	registrationservice "github.com/letsssgooo/giveaways_bot/internal/services/participation/registration"
	promotionservice "github.com/letsssgooo/giveaways_bot/internal/services/promotion"
	"github.com/letsssgooo/giveaways_bot/tests/utils"
)

type participationFixture struct {
	db      *pgxpool.Pool
	day     int64
	updates atomic.Int64
}

func participationNew(t *testing.T, codes int) *participationFixture {
	t.Helper()
	// Moscow is already on January 16 while the UTC session is on January 15.
	// This catches accidentally using the database session date for admission.
	f := &participationFixture{db: utils.FreezeClock(t, utils.NewDB(t), time.Date(2030, 1, 15, 21, 30, 0, 0, time.UTC))}
	utils.Exec(t, f.db, `INSERT INTO promotions(name, starts_on, ends_on)
		VALUES ('Integration', (clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date - 20,
			(clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date + 20);
		INSERT INTO partners(name, telegram_chat_id, subscription_url)
		VALUES ('Partner 1', -10001, 'https://t.me/integration1');
		INSERT INTO users(telegram_user_id, chat_id) VALUES (101,101),(202,202);`)
	if err := f.db.QueryRow(t.Context(), `INSERT INTO partner_days(partner_id, date, opens_at, closes_at)
		SELECT 1, today, today::timestamp AT TIME ZONE 'Europe/Moscow',
			(today+1)::timestamp AT TIME ZONE 'Europe/Moscow'
		FROM (SELECT (clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date AS today) AS moment
		RETURNING id`).Scan(&f.day); err != nil {
		t.Fatal(err)
	}
	utils.Exec(t, f.db, `INSERT INTO promo_codes(partner_id, code)
		SELECT 1, 'CODE-' || number FROM generate_series(1,$1::integer) AS number`, codes)
	return f
}

func (f *participationFixture) code(ctx context.Context, userID, dayID int64) (entity.CodeResult, error) {
	return f.codeUpdate(ctx, userID, dayID, f.updates.Add(1))
}

func (f *participationFixture) codeUpdate(ctx context.Context, userID, dayID, updateID int64) (entity.CodeResult, error) {
	var result entity.CodeResult
	err := postgres.InTx(ctx, f.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO telegram_updates(update_id, payload) VALUES ($1,'{}')
			ON CONFLICT (update_id) DO NOTHING`, updateID); err != nil {
			return err
		}
		var err error
		result, err = codeservice.New(coderepo.New(tx)).RequestCode(ctx, userID, dayID, updateID)
		return err
	})
	return result, err
}

func (f *participationFixture) prepare(ctx context.Context, client *telegram.Client, telegramID, dayID int64) (registrationentity.Verification, error) {
	return registrationservice.NewRegistration(registrationrepo.NewRegistration(f.db), client).Prepare(ctx, telegramID, dayID)
}

func (f *participationFixture) register(ctx context.Context, userID, telegramID, dayID int64, verification registrationentity.Verification) (registrationentity.RegistrationResult, error) {
	var result registrationentity.RegistrationResult
	err := postgres.InTx(ctx, f.db, func(tx pgx.Tx) error {
		var err error
		result, err = registrationservice.NewRegistration(registrationrepo.NewRegistration(tx), nil).
			Register(ctx, userID, telegramID, dayID, verification)
		return err
	})
	return result, err
}

func (f *participationFixture) verifiedRegistration(t *testing.T, client *telegram.Client, userID, telegramID, dayID int64) registrationentity.RegistrationResult {
	t.Helper()
	verification, err := f.prepare(t.Context(), client, telegramID, dayID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.register(t.Context(), userID, telegramID, dayID, verification)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *participationFixture) counts(t *testing.T, userID int64, codes, registrations, active int) {
	t.Helper()
	var gotCodes, gotRegistrations, gotActive int
	err := f.db.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM promo_codes WHERE user_id=$1),
		(SELECT count(*) FROM registrations WHERE user_id=$1),
		(SELECT count(*) FROM active_days WHERE user_id=$1)`, userID).
		Scan(&gotCodes, &gotRegistrations, &gotActive)
	if err != nil {
		t.Fatal(err)
	}
	if gotCodes != codes || gotRegistrations != registrations || gotActive != active {
		t.Fatalf("user %d: codes/registrations/active = %d/%d/%d, want %d/%d/%d",
			userID, gotCodes, gotRegistrations, gotActive, codes, registrations, active)
	}
}

// The only substituted component is Telegram's HTTP server. The real client
// still checks the bot's channel permissions and parses getChatMember replies.
func participationTelegram(t *testing.T, member func(http.ResponseWriter, *http.Request, int64)) *telegram.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/getChatMember") {
			t.Errorf("unexpected Telegram request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		userID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
		if err != nil {
			t.Error(err)
			return
		}
		if userID == 12345 {
			participationMember(t, w, userID, "administrator")
			return
		}
		if member == nil {
			participationMember(t, w, userID, "member")
			return
		}
		member(w, r, userID)
	}))
	t.Cleanup(server.Close)
	client, err := telegram.NewWithBaseURL("12345:integration-test", time.Second, 6*time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func participationMember(t *testing.T, w http.ResponseWriter, userID int64, status string) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "result": map[string]any{"status": status, "user": map[string]any{"id": userID}},
	}); err != nil {
		t.Error(err)
	}
}

func TestParticipationCodeReplayAndConcurrentRequests(t *testing.T) {
	f := participationNew(t, 20)
	first, err := f.code(t.Context(), 1, f.day)
	if err != nil || first.Outcome != entity.CodeIssued || first.Code == "" || first.Chances != 1 {
		t.Fatalf("first issue = %+v, %v", first, err)
	}
	replay, err := f.codeUpdate(t.Context(), 1, f.day, 1)
	if err != nil || replay.Outcome != entity.CodeIssued || replay.Code != first.Code {
		t.Fatalf("same update replay = %+v, %v", replay, err)
	}
	const parallelRequests = 12
	start := make(chan struct{})
	errors := make(chan error, parallelRequests)
	var wg sync.WaitGroup
	for range parallelRequests {
		wg.Go(func() {
			<-start
			result, err := f.code(t.Context(), 2, f.day)
			if err == nil && (result.Outcome != entity.CodeIssued && result.Outcome != entity.CodeRepeated || result.Code == first.Code || result.Chances != 1) {
				err = fmt.Errorf("concurrent issue = %+v", result)
			}
			errors <- err
		})
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Error(err)
		}
	}
	f.counts(t, 1, 1, 0, 1)
	f.counts(t, 2, 1, 0, 1)
	var requests, codes int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*), count(DISTINCT code.code)
		FROM promo_code_requests AS request JOIN promo_codes AS code
		ON code.user_id=request.user_id AND code.partner_day_id=request.partner_day_id`).Scan(&requests, &codes); err != nil {
		t.Fatal(err)
	}
	if requests != parallelRequests+1 || codes != 2 {
		t.Fatalf("persisted requests=%d distinct codes=%d", requests, codes)
	}
}

func TestParticipationLastCodeHasOneRecipient(t *testing.T) {
	f := participationNew(t, 1)
	start := make(chan struct{})
	results := make(chan entity.CodeResult, 2)
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	for _, userID := range []int64{1, 2} {
		wg.Go(func() {
			<-start
			result, err := f.code(t.Context(), userID, f.day)
			results <- result
			errors <- err
		})
	}
	close(start)
	wg.Wait()
	var issued, exhausted int
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		switch result := <-results; result.Outcome {
		case entity.CodeIssued:
			issued++
		case entity.CodeExhausted:
			exhausted++
		default:
			t.Fatalf("unexpected last-code result: %+v", result)
		}
	}
	var assigned, active int
	if err := f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM promo_codes WHERE user_id IS NOT NULL),
		(SELECT count(*) FROM active_days)`).Scan(&assigned, &active); err != nil {
		t.Fatal(err)
	}
	if issued != 1 || exhausted != 1 || assigned != 1 || active != 1 {
		t.Fatalf("issued/exhausted/assigned/active = %d/%d/%d/%d", issued, exhausted, assigned, active)
	}
}

func TestParticipationExhaustionDoesNotBlockRegistration(t *testing.T) {
	f := participationNew(t, 0)
	code, err := f.code(t.Context(), 1, f.day)
	if err != nil || code.Outcome != entity.CodeExhausted || code.Code != "" || code.Chances != 0 {
		t.Fatalf("empty pool = %+v, %v", code, err)
	}
	f.counts(t, 1, 0, 0, 0)
	registration := f.verifiedRegistration(t, participationTelegram(t, nil), 1, 101, f.day)
	if registration.Status != registrationentity.Registered || registration.Chances != 1 {
		t.Fatalf("registration after exhaustion = %+v", registration)
	}
	f.counts(t, 1, 0, 1, 1)
}

func TestParticipationSubscriptionFailuresDoNotRegister(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		want   registrationentity.RegistrationStatus
	}{
		{"not_subscribed", "left", registrationentity.NotSubscribed},
		{"telegram_failure", "error", registrationentity.VerificationFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := participationNew(t, 0)
			client := participationTelegram(t, func(w http.ResponseWriter, _ *http.Request, userID int64) {
				if tc.status == "error" {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"ok":false,"error_code":503,"description":"temporarily unavailable"}`))
					return
				}
				participationMember(t, w, userID, tc.status)
			})
			result := f.verifiedRegistration(t, client, 1, 101, f.day)
			if result.Status != tc.want || result.Chances != 0 {
				t.Fatalf("registration = %+v, want %s", result, tc.want)
			}
			f.counts(t, 1, 0, 0, 0)
		})
	}
}

func TestParticipationRegistrationConcurrencyAndUnsubscribe(t *testing.T) {
	f := participationNew(t, 0)
	var unsubscribed atomic.Bool
	var checks atomic.Int64
	client := participationTelegram(t, func(w http.ResponseWriter, _ *http.Request, userID int64) {
		checks.Add(1)
		status := "member"
		if unsubscribed.Load() {
			status = "left"
		}
		participationMember(t, w, userID, status)
	})
	verification, err := f.prepare(t.Context(), client, 101, f.day)
	if err != nil || !verification.Subscribed {
		t.Fatalf("verification = %+v, %v", verification, err)
	}
	const requests = 10
	start := make(chan struct{})
	results := make(chan registrationentity.RegistrationStatus, requests)
	errors := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			<-start
			result, err := f.register(t.Context(), 1, 101, f.day, verification)
			results <- result.Status
			errors <- err
		})
	}
	close(start)
	wg.Wait()
	var created, repeated int
	for range requests {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		switch status := <-results; status {
		case registrationentity.Registered:
			created++
		case registrationentity.AlreadyRegistered:
			repeated++
		default:
			t.Fatalf("concurrent status = %s", status)
		}
	}
	if created != 1 || repeated != requests-1 {
		t.Fatalf("created=%d repeated=%d", created, repeated)
	}
	var registeredAt, checkedAt time.Time
	if err := f.db.QueryRow(t.Context(), `SELECT registered_at, subscription_checked_at FROM registrations
		WHERE user_id=1 AND partner_day_id=$1`, f.day).Scan(&registeredAt, &checkedAt); err != nil {
		t.Fatal(err)
	}
	unsubscribed.Store(true)
	result := f.verifiedRegistration(t, client, 1, 101, f.day)
	if result.Status != registrationentity.AlreadyRegistered || checks.Load() != 1 {
		t.Fatalf("repeat after unsubscribe = %+v, subscription checks=%d", result, checks.Load())
	}
	var unchanged bool
	if err := f.db.QueryRow(t.Context(), `SELECT registered_at=$2 AND subscription_checked_at=$3
		FROM registrations WHERE user_id=1 AND partner_day_id=$1`, f.day, registeredAt, checkedAt).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if !unchanged || !checkedAt.Equal(verification.CheckedAt) {
		t.Fatal("repeat changed the initial registration timestamps")
	}
	f.counts(t, 1, 0, 1, 1)
}

func TestParticipationActiveDayAcrossActionsAndPartners(t *testing.T) {
	for _, registrationFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("registration_first_%v", registrationFirst), func(t *testing.T) {
			f := participationNew(t, 3)
			client := participationTelegram(t, nil)
			utils.Exec(t, f.db, `INSERT INTO partners(name,telegram_chat_id,subscription_url)
				VALUES ('Partner 2', -10002, 'https://t.me/integration2');
				INSERT INTO promo_codes(partner_id,code) VALUES (2,'OTHER-PARTNER');`)
			var otherDay int64
			if err := f.db.QueryRow(t.Context(), `INSERT INTO partner_days(partner_id,date,opens_at,closes_at)
				SELECT 2,date,opens_at,closes_at FROM partner_days WHERE id=$1 RETURNING id`, f.day).Scan(&otherDay); err != nil {
				t.Fatal(err)
			}
			register := func(day int64) {
				t.Helper()
				result := f.verifiedRegistration(t, client, 1, 101, day)
				if result.Status != registrationentity.Registered || result.Chances != 1 {
					t.Fatalf("registration = %+v", result)
				}
			}
			code := func(day int64) {
				t.Helper()
				result, err := f.code(t.Context(), 1, day)
				if err != nil || result.Outcome != entity.CodeIssued || result.Chances != 1 {
					t.Fatalf("code = %+v, %v", result, err)
				}
			}
			if registrationFirst {
				register(f.day)
				code(f.day)
			} else {
				code(f.day)
				register(f.day)
			}
			code(otherDay)
			register(otherDay)
			if err := postgres.InTx(t.Context(), f.db, func(tx pgx.Tx) error {
				chances, err := promotionservice.New(promotionrepo.New(tx)).Chances(t.Context(), 1)
				if err == nil && (chances.Count != 1 || len(chances.ActiveDays) != 1 || len(chances.ActiveDays[0].Partners) != 2) {
					return fmt.Errorf("cross-partner history = %+v", chances)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			f.counts(t, 1, 2, 2, 1)
		})
	}
}

func TestParticipationChanceLimitKeepsFullHistory(t *testing.T) {
	for _, precedingDays := range []int{1, 9, 10} {
		t.Run(fmt.Sprintf("preceding_days_%d", precedingDays), func(t *testing.T) {
			f := participationNew(t, 1)
			// Historical fixtures represent previous real calendar days; today's
			// activation is performed through the actual service and transaction.
			utils.Exec(t, f.db, `INSERT INTO active_days(user_id,date,activated_at)
				SELECT 1,today-number,(today-number)::timestamp AT TIME ZONE 'Europe/Moscow'
				FROM (SELECT date AS today FROM partner_days WHERE id=$1) AS day
				CROSS JOIN generate_series(1,$2::integer) AS number`, f.day, precedingDays)
			code, err := f.code(t.Context(), 1, f.day)
			want := min(precedingDays+1, entity.MaxFinalChances)
			if err != nil || code.Chances != want {
				t.Fatalf("code chances = %+v, %v; want %d", code, err, want)
			}
			registration := f.verifiedRegistration(t, participationTelegram(t, nil), 1, 101, f.day)
			if registration.Status != registrationentity.Registered || registration.Chances != want {
				t.Fatalf("registration chances = %+v, want %d", registration, want)
			}
			if err := postgres.InTx(t.Context(), f.db, func(tx pgx.Tx) error {
				for range 2 {
					chances, err := promotionservice.New(promotionrepo.New(tx)).Chances(t.Context(), 1)
					if err != nil {
						return err
					}
					if chances.Count != want || len(chances.ActiveDays) != precedingDays+1 {
						return fmt.Errorf("capped chances/full history = %+v", chances)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			f.counts(t, 1, 1, 1, precedingDays+1)
		})
	}
}

func TestParticipationConcurrentCodeAndRegistrationShareActiveDay(t *testing.T) {
	f := participationNew(t, 10)
	verification, err := f.prepare(t.Context(), participationTelegram(t, nil), 101, f.day)
	if err != nil || !verification.Subscribed {
		t.Fatalf("prepare = %+v, %v", verification, err)
	}
	const requests = 12
	start := make(chan struct{})
	errors := make(chan error, requests)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Go(func() {
			<-start
			if i%2 == 0 {
				result, err := f.code(t.Context(), 1, f.day)
				if err == nil && (result.Chances != 1 || result.Outcome != entity.CodeIssued && result.Outcome != entity.CodeRepeated) {
					err = fmt.Errorf("concurrent code = %+v", result)
				}
				errors <- err
				return
			}
			result, err := f.register(t.Context(), 1, 101, f.day, verification)
			if err == nil && (result.Chances != 1 || result.Status != registrationentity.Registered && result.Status != registrationentity.AlreadyRegistered) {
				err = fmt.Errorf("concurrent registration = %+v", result)
			}
			errors <- err
		})
	}
	close(start)
	wg.Wait()
	for range requests {
		if err := <-errors; err != nil {
			t.Error(err)
		}
	}
	f.counts(t, 1, 1, 1, 1)
}

func TestParticipationCampaignBoundaryDatesAreInclusive(t *testing.T) {
	f := participationNew(t, 1)
	utils.Exec(t, f.db, `UPDATE promotions SET starts_on=(clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date,
		ends_on=(clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date`)
	result, err := f.code(t.Context(), 1, f.day)
	if err != nil || result.Outcome != entity.CodeIssued {
		t.Fatalf("code on first/last campaign date = %+v, %v", result, err)
	}
	registration := f.verifiedRegistration(t, participationTelegram(t, nil), 1, 101, f.day)
	if registration.Status != registrationentity.Registered {
		t.Fatalf("registration on first/last campaign date = %+v", registration)
	}
	f.counts(t, 1, 1, 1, 1)
}

func TestParticipationAdmissionAndHistoricalAccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change string
	}{
		{"disabled", `UPDATE partner_days SET is_enabled=false`},
		{"closed", `UPDATE partner_days SET closes_at=clock_timestamp()`},
		{"campaign_ended", `UPDATE promotions SET ends_on=(clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date-1`},
		{"campaign_not_started", `UPDATE promotions SET starts_on=(clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date+1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := participationNew(t, 2)
			client := participationTelegram(t, nil)
			oldCode, err := f.code(t.Context(), 1, f.day)
			if err != nil || oldCode.Outcome != entity.CodeIssued {
				t.Fatalf("original code = %+v, %v", oldCode, err)
			}
			if registration := f.verifiedRegistration(t, client, 1, 101, f.day); registration.Status != registrationentity.Registered {
				t.Fatalf("original registration = %+v", registration)
			}
			utils.Exec(t, f.db, tc.change)
			newCode, err := f.code(t.Context(), 2, f.day)
			if err != nil || newCode.Outcome != entity.CodeClosed {
				t.Fatalf("new code = %+v, %v", newCode, err)
			}
			if registration := f.verifiedRegistration(t, client, 2, 202, f.day); registration.Status != registrationentity.Closed {
				t.Fatalf("new registration = %+v", registration)
			}
			repeat, err := f.code(t.Context(), 1, f.day)
			if err != nil || repeat.Outcome != entity.CodeRepeated || repeat.Code != oldCode.Code {
				t.Fatalf("historical code = %+v, %v", repeat, err)
			}
			if registration := f.verifiedRegistration(t, client, 1, 101, f.day); registration.Status != registrationentity.AlreadyRegistered {
				t.Fatalf("historical registration = %+v", registration)
			}
			if err := postgres.InTx(t.Context(), f.db, func(tx pgx.Tx) error {
				options, err := codeservice.New(coderepo.New(tx)).CodeOptions(t.Context(), 1)
				if err == nil && (len(options) != 1 || !options[0].HasCode) {
					return fmt.Errorf("historical code options = %+v", options)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			f.counts(t, 1, 1, 1, 1)
			f.counts(t, 2, 0, 0, 0)
		})
	}
}

func TestParticipationMoscowDatesAndOpening(t *testing.T) {
	f := participationNew(t, 3)
	client := participationTelegram(t, nil)
	// The DB session is UTC. All admission dates must still use Moscow.
	var today time.Time
	if err := f.db.QueryRow(t.Context(), `SELECT (clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date`).Scan(&today); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{-1, 1} {
		var dayID int64
		if err := f.db.QueryRow(t.Context(), `INSERT INTO partner_days(partner_id,date,opens_at,closes_at)
			SELECT 1,date+$2::integer,(date+$2::integer)::timestamp AT TIME ZONE 'Europe/Moscow',
				(date+$2::integer+1)::timestamp AT TIME ZONE 'Europe/Moscow'
			FROM partner_days WHERE id=$1 RETURNING id`, f.day, offset).Scan(&dayID); err != nil {
			t.Fatal(err)
		}
		result, err := f.code(t.Context(), 1, dayID)
		if err != nil || result.Outcome != entity.CodeClosed {
			t.Fatalf("other Moscow date %d = %+v, %v", offset, result, err)
		}
		if result := f.verifiedRegistration(t, client, 1, 101, dayID); result.Status != registrationentity.Closed {
			t.Fatalf("other-date registration = %+v", result)
		}
	}
	// Use a future opening, then hit the exact inclusive start and exclusive
	// end through the isolated SQL clock. The real query predicates are tested.
	utils.Exec(t, f.db, `UPDATE partner_days SET opens_at=clock_timestamp()+(closes_at-clock_timestamp())/2,
		closes_at=closes_at-interval '1 hour' WHERE id=$1`, f.day)
	closed, err := f.code(t.Context(), 1, f.day)
	if err != nil || closed.Outcome != entity.CodeClosed {
		t.Fatalf("before opening = %+v, %v", closed, err)
	}
	if result := f.verifiedRegistration(t, client, 1, 101, f.day); result.Status != registrationentity.Closed {
		t.Fatalf("registration before opening = %+v", result)
	}
	var opensAt, closesAt time.Time
	if err := f.db.QueryRow(t.Context(), `SELECT opens_at,closes_at FROM partner_days WHERE id=$1`, f.day).Scan(&opensAt, &closesAt); err != nil {
		t.Fatal(err)
	}
	utils.SetTime(t, f.db, opensAt)
	opened, err := f.code(t.Context(), 1, f.day)
	if err != nil || opened.Outcome != entity.CodeIssued || !opened.Date.Equal(today) {
		t.Fatalf("at exact opening = %+v, %v; Moscow date %s", opened, err, today)
	}
	if result := f.verifiedRegistration(t, client, 1, 101, f.day); result.Status != registrationentity.Registered {
		t.Fatalf("registration after opening = %+v", result)
	}
	f.counts(t, 1, 1, 1, 1)
	utils.SetTime(t, f.db, closesAt)
	atClose, err := f.code(t.Context(), 2, f.day)
	if err != nil || atClose.Outcome != entity.CodeClosed {
		t.Fatalf("at exact closing = %+v, %v", atClose, err)
	}
	if result := f.verifiedRegistration(t, client, 2, 202, f.day); result.Status != registrationentity.Closed {
		t.Fatalf("registration at exact closing = %+v", result)
	}
	f.counts(t, 2, 0, 0, 0)
}

func TestParticipationRechecksAfterSubscriptionRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change string
		want   registrationentity.RegistrationStatus
	}{
		{"window_closed", `UPDATE partner_days SET closes_at=clock_timestamp()`, registrationentity.Closed},
		{"day_disabled", `UPDATE partner_days SET is_enabled=false`, registrationentity.Closed},
		{"subscription_channel_changed", `UPDATE partners SET telegram_chat_id=-10099`, registrationentity.VerificationFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := participationNew(t, 0)
			requested, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			client := participationTelegram(t, func(w http.ResponseWriter, r *http.Request, userID int64) {
				close(requested)
				select {
				case <-release:
					participationMember(t, w, userID, "member")
				case <-r.Context().Done():
				}
			})
			defer unblock()
			type prepared struct {
				verification registrationentity.Verification
				err          error
			}
			done := make(chan prepared, 1)
			go func() {
				verification, err := f.prepare(t.Context(), client, 101, f.day)
				done <- prepared{verification, err}
			}()
			select {
			case <-requested:
			case <-time.After(5 * time.Second):
				t.Fatal("subscription request did not reach Telegram")
			}
			utils.Exec(t, f.db, tc.change)
			unblock()
			preparedResult := <-done
			if preparedResult.err != nil || !preparedResult.verification.Subscribed {
				t.Fatalf("prepare = %+v, %v", preparedResult.verification, preparedResult.err)
			}
			result, err := f.register(t.Context(), 1, 101, f.day, preparedResult.verification)
			if err != nil || result.Status != tc.want {
				t.Fatalf("changed configuration = %+v, %v; want %s", result, err, tc.want)
			}
			f.counts(t, 1, 0, 0, 0)
		})
	}
}

func TestParticipationRechecksWindowAfterRowLock(t *testing.T) {
	for _, action := range []string{"code_user_lock", "code_stock_lock", "registration"} {
		t.Run(action, func(t *testing.T) {
			f := participationNew(t, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			verification, err := f.prepare(ctx, participationTelegram(t, nil), 101, f.day)
			if err != nil || !verification.Subscribed {
				t.Fatalf("prepare before locking = %+v, %v", verification, err)
			}
			var closesAt time.Time
			if err := f.db.QueryRow(ctx, `UPDATE partner_days SET closes_at=clock_timestamp()+interval '1 hour'
				WHERE id=$1 RETURNING closes_at`, f.day).Scan(&closesAt); err != nil {
				t.Fatal(err)
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			var blockerPID int
			lockQuery := `SELECT pg_backend_pid() FROM users WHERE id=1 FOR NO KEY UPDATE`
			if action == "code_stock_lock" {
				lockQuery = `SELECT pg_backend_pid() FROM promo_codes WHERE id=1 FOR UPDATE`
			}
			if err := blocker.QueryRow(ctx, lockQuery).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if action != "registration" {
					result, err := f.code(ctx, 1, f.day)
					if err == nil && result.Outcome != entity.CodeClosed {
						err = fmt.Errorf("code admitted after waiting: %+v", result)
					}
					done <- err
					return
				}
				result, err := f.register(ctx, 1, 101, f.day, verification)
				if err == nil && result.Status != registrationentity.Closed {
					err = fmt.Errorf("registration admitted after waiting: %+v", result)
				}
				done <- err
			}()
			// Observe the actual PostgreSQL lock dependency before advancing to
			// the deadline. Scheduling delays cannot accidentally skip the race.
			for {
				var blocked bool
				if err := f.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
					WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				runtime.Gosched()
			}
			utils.SetTime(t, f.db, closesAt)
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			f.counts(t, 1, 0, 0, 0)
		})
	}
}
