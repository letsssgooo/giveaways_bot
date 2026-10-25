//go:build integration

package tests_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	userhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users"
	telegramclient "github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/notifications"
	participationrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/participation"
	registrationrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/participation/registration"
	promotionrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/updates"
	usersrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/users"
	deliveryworker "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/delivery"
	participationservice "github.com/letsssgooo/giveaways_bot/internal/services/participation"
	registrationservice "github.com/letsssgooo/giveaways_bot/internal/services/participation/registration"
	promotionservice "github.com/letsssgooo/giveaways_bot/internal/services/promotion"
	usersservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
	"github.com/letsssgooo/giveaways_bot/tests/utils"
)

func deliveryHandleUser(ctx context.Context, tx pgx.Tx, event updates.Event) error {
	var update models.Update
	if err := json.Unmarshal(event.Payload, &update); err != nil {
		return err
	}
	return userhandler.New(usersservice.New(usersrepo.New(tx)), promotionservice.New(promotionrepo.New(tx)),
		participationservice.New(participationrepo.New(tx)), notifications.New(tx), "integrationbot").Handle(ctx, &update)
}

func deliveryWait(t *testing.T, db *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var ready bool
		if err := db.QueryRow(ctx, query, args...).Scan(&ready); err != nil {
			t.Fatalf("wait for delivery state: %v", err)
		}
		if ready {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("delivery state did not converge: %s", query)
		}
	}
}

func startDelivery(t *testing.T, db *pgxpool.Pool, client *telegramclient.Client) func() {
	t.Helper()
	intake, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- (deliveryworker.Delivery{Sender: client, Queue: notifications.NewQueue(db), LeaseDuration: time.Second,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Run(intake, t.Context())
	}()
	var once sync.Once
	finish := func() {
		once.Do(func() {
			stop()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("delivery worker: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("delivery worker did not stop")
			}
		})
	}
	t.Cleanup(finish)
	return finish
}

func deliveryClient(t *testing.T, handler http.HandlerFunc) *telegramclient.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := telegramclient.NewWithBaseURL("integration:token", time.Second, 3*time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func seedDeliveryMessage(t *testing.T, db *pgxpool.Pool, telegramID, updateID int64) int64 {
	t.Helper()
	var userID, messageID int64
	if err := db.QueryRow(t.Context(), `INSERT INTO users(telegram_user_id,chat_id) VALUES($1,$1) RETURNING id`, telegramID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	utils.Exec(t, db, `INSERT INTO telegram_updates(update_id,payload,processed_at) VALUES($1,'{}',clock_timestamp())`, updateID)
	adminTx(t, db, func(tx pgx.Tx) error {
		payload, err := json.Marshal(map[string]any{"chat_id": telegramID, "text": "Integration message"})
		if err != nil {
			return err
		}
		return notifications.New(tx).Enqueue(t.Context(), userID, updateID, protocol.MessageKindText, payload, fmt.Sprintf("integration:%d", updateID))
	})
	if err := db.QueryRow(t.Context(), `SELECT id FROM outbox_messages WHERE update_id=$1`, updateID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	return messageID
}

func TestUpdateAndBusinessOperationRollbackWhenResponseCannotBeSaved(t *testing.T) {
	db := utils.NewDB(t)
	f := seedAdministration(t, db)
	utils.Exec(t, db, `INSERT INTO promo_codes(partner_id,code) VALUES($1,'PROMO-ATOMIC')`, f.partner)
	update := adminCallback(1, 200, 200, models.ChatTypePrivate, fmt.Sprintf("partner:code:%d", f.day))
	payload, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	inbox := updates.New(db)
	if err := inbox.Save(t.Context(), 0, []updates.Event{{ID: 1, Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	// Fail the actual SQL response INSERT after code assignment, activity and callback acknowledgment.
	utils.Exec(t, db, `CREATE FUNCTION reject_test_response() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.kind='send_message' THEN RAISE EXCEPTION 'injected outbox write failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_test_response BEFORE INSERT ON outbox_messages FOR EACH ROW EXECUTE FUNCTION reject_test_response()`)
	processed, err := inbox.ProcessNext(t.Context(), deliveryHandleUser)
	if err == nil || processed || !strings.Contains(err.Error(), "injected outbox write failure") {
		t.Fatalf("failed response write: processed=%v err=%v", processed, err)
	}
	if got := adminScalar(t, db, `SELECT (SELECT count(*) FROM promo_codes WHERE user_id IS NOT NULL)+
		(SELECT count(*) FROM promo_code_requests)+(SELECT count(*) FROM active_days)+(SELECT count(*) FROM outbox_messages)`); got != 0 {
		t.Fatalf("rolled-back update left %d business/outbox rows", got)
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM telegram_updates WHERE update_id=1 AND processed_at IS NULL`); got != 1 {
		t.Fatal("failed event was not retained for retry")
	}
	utils.Exec(t, db, `DROP TRIGGER reject_test_response ON outbox_messages; DROP FUNCTION reject_test_response()`)
	// Reconstructing the repository simulates retry after process restart.
	processed, err = updates.New(db).ProcessNext(t.Context(), deliveryHandleUser)
	if err != nil || !processed {
		t.Fatalf("retry: processed=%v err=%v", processed, err)
	}
	for name, query := range map[string]string{
		"code":            `SELECT count(*) FROM promo_codes WHERE code='PROMO-ATOMIC' AND user_id IS NOT NULL`,
		"request":         `SELECT count(*) FROM promo_code_requests`,
		"active day":      `SELECT count(*) FROM active_days`,
		"processed event": `SELECT count(*) FROM telegram_updates WHERE processed_at IS NOT NULL`,
	} {
		if got := adminScalar(t, db, query); got != 1 {
			t.Fatalf("retry %s count=%d, want 1", name, got)
		}
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM outbox_messages`); got != 2 {
		t.Fatalf("response and callback acknowledgment count=%d, want 2", got)
	}
	if err := updates.New(db).Save(t.Context(), 2, []updates.Event{{ID: 1, Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	processed, err = updates.New(db).ProcessNext(t.Context(), deliveryHandleUser)
	if err != nil || processed {
		t.Fatalf("duplicate event processed again: processed=%v err=%v", processed, err)
	}

	// A permanent Telegram failure happens after commit and must not undo issuance.
	client := deliveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`))
	})
	stop := startDelivery(t, db, client)
	deliveryWait(t, db, `SELECT EXISTS(SELECT 1 FROM outbox_messages WHERE kind=$1 AND status='failed')`, protocol.MessageKindText)
	stop()
	if got := adminScalar(t, db, `SELECT (SELECT count(*) FROM promo_codes WHERE user_id IS NOT NULL)+
		(SELECT count(*) FROM promo_code_requests)+(SELECT count(*) FROM active_days)`); got != 3 {
		t.Fatalf("post-commit delivery failure changed business state: %d rows", got)
	}
}

func TestInboxBatchAndOffsetAreAtomicAndReplaysDoNotDuplicate(t *testing.T) {
	db := utils.NewDB(t)
	inbox := updates.New(db)
	err := inbox.Save(t.Context(), 0, []updates.Event{{ID: 10, Payload: json.RawMessage(`{"update_id":10}`)}, {ID: 11, Payload: json.RawMessage(`not-json`)}})
	if err == nil {
		t.Fatal("invalid event was accepted")
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM telegram_updates`); got != 0 {
		t.Fatalf("partial inbox batch committed: %d", got)
	}
	if offset, err := inbox.Offset(t.Context()); err != nil || offset != 0 {
		t.Fatalf("rolled-back offset=%d err=%v", offset, err)
	}
	events := []updates.Event{{ID: 11, Payload: json.RawMessage(`{"update_id":11}`)}, {ID: 10, Payload: json.RawMessage(`{"update_id":10}`)}}
	if err := inbox.Save(t.Context(), 0, events); err != nil {
		t.Fatal(err)
	}
	if err := updates.New(db).Save(t.Context(), 0, events[:1]); err != nil {
		t.Fatal(err)
	}
	if offset, err := inbox.Offset(t.Context()); err != nil || offset != 12 {
		t.Fatalf("replayed/stale offset=%d err=%v", offset, err)
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM telegram_updates`); got != 2 {
		t.Fatalf("duplicate event count=%d", got)
	}
}

func TestRegistrationAndResponseAreAtomicAndSurviveDeliveryFailure(t *testing.T) {
	db := utils.NewDB(t)
	f := seedAdministration(t, db)
	client := participationTelegram(t, nil)
	verification, err := registrationservice.NewRegistration(registrationrepo.NewRegistration(db), client).Prepare(t.Context(), 200, f.day)
	if err != nil || !verification.Subscribed {
		t.Fatalf("real subscription verification=%+v error=%v", verification, err)
	}
	update := adminCallback(1, 200, 200, models.ChatTypePrivate, fmt.Sprintf("registration:check:%d", f.day))
	payload, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	if err := updates.New(db).Save(t.Context(), 0, []updates.Event{{ID: 1, Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	handle := func(ctx context.Context, tx pgx.Tx, event updates.Event) error {
		var eventUpdate models.Update
		if err := json.Unmarshal(event.Payload, &eventUpdate); err != nil {
			return err
		}
		return userhandler.New(usersservice.New(usersrepo.New(tx)), promotionservice.New(promotionrepo.New(tx)),
			participationservice.New(participationrepo.New(tx)), notifications.New(tx), "integrationbot").
			WithRegistration(registrationservice.NewRegistration(registrationrepo.NewRegistration(tx), nil), verification).
			Handle(ctx, &eventUpdate)
	}
	utils.Exec(t, db, `CREATE FUNCTION reject_registration_response() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.kind='send_message' THEN RAISE EXCEPTION 'injected registration response failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_registration_response BEFORE INSERT ON outbox_messages FOR EACH ROW EXECUTE FUNCTION reject_registration_response()`)
	processed, err := updates.New(db).ProcessNext(t.Context(), handle)
	if err == nil || processed || !strings.Contains(err.Error(), "injected registration response failure") {
		t.Fatalf("response failure processed=%v error=%v", processed, err)
	}
	if got := adminScalar(t, db, `SELECT (SELECT count(*) FROM registrations)+(SELECT count(*) FROM active_days)+
		(SELECT count(*) FROM outbox_messages)+(SELECT count(*) FROM telegram_updates WHERE processed_at IS NOT NULL)`); got != 0 {
		t.Fatalf("registration failure left %d committed rows", got)
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM telegram_updates WHERE processed_at IS NULL`); got != 1 {
		t.Fatal("registration event disappeared after rollback")
	}
	utils.Exec(t, db, `DROP TRIGGER reject_registration_response ON outbox_messages; DROP FUNCTION reject_registration_response()`)
	processed, err = updates.New(db).ProcessNext(t.Context(), handle)
	if err != nil || !processed {
		t.Fatalf("registration retry processed=%v error=%v", processed, err)
	}
	var registrationTime time.Time
	if err := db.QueryRow(t.Context(), `SELECT registered_at FROM registrations`).Scan(&registrationTime); err != nil {
		t.Fatal(err)
	}
	delivery := deliveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":503,"description":"Unavailable"}`))
	})
	stop := startDelivery(t, db, delivery)
	deliveryWait(t, db, `SELECT EXISTS(SELECT 1 FROM outbox_messages WHERE kind=$1 AND status='pending' AND attempts=1 AND attempt_token IS NULL)`, protocol.MessageKindText)
	stop()
	if got := adminScalar(t, db, `SELECT count(*) FROM registrations WHERE registered_at=$1`, registrationTime); got != 1 {
		t.Fatal("post-commit delivery failure lost original registration")
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM active_days`); got != 1 {
		t.Fatalf("post-commit delivery failure changed chances: %d", got)
	}
	processed, err = updates.New(db).ProcessNext(t.Context(), handle)
	if err != nil || processed {
		t.Fatalf("committed registration event replayed: processed=%v error=%v", processed, err)
	}
}

func TestDeliveryLeaseRecoveryRejectsStaleOwner(t *testing.T) {
	db := utils.NewDB(t)
	id := seedDeliveryMessage(t, db, 100, 1)
	queue := notifications.NewQueue(db)
	first, err := queue.Claim(t.Context(), time.Minute)
	if err != nil || first == nil || first.ID != id || first.Attempts != 1 {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	if other, err := notifications.NewQueue(db).Claim(t.Context(), time.Minute); err != nil || other != nil {
		t.Fatalf("live lease was stolen: %+v %v", other, err)
	}
	utils.Exec(t, db, `UPDATE outbox_messages SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, id)
	restarted := notifications.NewQueue(db)
	second, err := restarted.Claim(t.Context(), time.Minute)
	if err != nil || second == nil || second.ID != id || second.Attempts != 2 || second.AttemptToken == first.AttemptToken {
		t.Fatalf("reclaimed=%+v err=%v", second, err)
	}
	for name, operation := range map[string]func() error{
		"sent":       func() error { return queue.Sent(t.Context(), id, first.AttemptToken) },
		"retry":      func() error { return queue.Retry(t.Context(), id, first.AttemptToken, 0, false) },
		"rate limit": func() error { return queue.RateLimited(t.Context(), id, first.AttemptToken, time.Minute) },
	} {
		if err := operation(); !errors.Is(err, notifications.ErrLeaseLost) {
			t.Fatalf("stale %s: %v", name, err)
		}
	}
	if err := restarted.Sent(t.Context(), id, second.AttemptToken); err != nil {
		t.Fatal(err)
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM outbox_messages WHERE id=$1 AND status='sent' AND attempt_token IS NULL AND lease_until IS NULL`, id); got != 1 {
		t.Fatal("new lease owner could not complete delivery")
	}
	if next, err := notifications.NewQueue(db).Claim(t.Context(), time.Minute); err != nil || next != nil {
		t.Fatalf("sent message reappeared after restart: %+v %v", next, err)
	}
}

func TestDeliveryHTTPFailuresAndRestart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		permanent bool
	}{
		{"temporary", http.StatusServiceUnavailable, false},
		{"permanent", http.StatusForbidden, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := utils.NewDB(t)
			id := seedDeliveryMessage(t, db, 100, 1)
			var calls atomic.Int64
			var success atomic.Bool
			client := deliveryClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if success.Load() {
					_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":100,"type":"private"}}}`))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"injected failure"}`, tc.status)
			})
			stop := startDelivery(t, db, client)
			status := "pending"
			if tc.permanent {
				status = "failed"
			}
			deliveryWait(t, db, `SELECT EXISTS(SELECT 1 FROM outbox_messages WHERE id=$1 AND status=$2 AND attempts=1 AND attempt_token IS NULL)`, id, status)
			stop()
			if calls.Load() != 1 {
				t.Fatalf("initial requests=%d", calls.Load())
			}
			if tc.permanent {
				if next, err := notifications.NewQueue(db).Claim(t.Context(), time.Minute); err != nil || next != nil {
					t.Fatalf("permanent failure retried: %+v %v", next, err)
				}
				return
			}
			success.Store(true)
			utils.Exec(t, db, `UPDATE outbox_messages SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1`, id)
			stopRestarted := startDelivery(t, db, client)
			deliveryWait(t, db, `SELECT EXISTS(SELECT 1 FROM outbox_messages WHERE id=$1 AND status='sent' AND attempts=2)`, id)
			stopRestarted()
			if calls.Load() != 2 {
				t.Fatalf("requests after restart=%d", calls.Load())
			}
		})
	}
}

func TestTelegramRateLimitPausesOtherChatsAcrossRestart(t *testing.T) {
	db := utils.NewDB(t)
	firstID := seedDeliveryMessage(t, db, 100, 1)
	secondID := seedDeliveryMessage(t, db, 200, 2)
	client := deliveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":60}}`))
	})
	stop := startDelivery(t, db, client)
	deliveryWait(t, db, `SELECT EXISTS(SELECT 1 FROM telegram_delivery_state WHERE blocked_until>clock_timestamp()+interval '30 seconds')`)
	stop()
	if got := adminScalar(t, db, `SELECT attempts FROM outbox_messages WHERE id=$1`, secondID); got != 0 {
		t.Fatalf("other chat attempted during global rate limit: %d", got)
	}
	// Make the messages eligible individually: the persisted global pause must still win.
	utils.Exec(t, db, `UPDATE outbox_messages SET available_at=clock_timestamp()-interval '1 second'`)
	if next, err := notifications.NewQueue(db).Claim(t.Context(), time.Minute); err != nil || next != nil {
		t.Fatalf("restart bypassed global pause: %+v %v", next, err)
	}
	utils.Exec(t, db, `UPDATE telegram_delivery_state SET blocked_until=clock_timestamp()-interval '1 second'`)
	queue := notifications.NewQueue(db)
	next, err := queue.Claim(t.Context(), time.Minute)
	if err != nil || next == nil || next.ID != firstID || next.Attempts != 2 {
		t.Fatalf("resume after pause=%+v err=%v", next, err)
	}
	if err := queue.Sent(t.Context(), next.ID, next.AttemptToken); err != nil {
		t.Fatal(err)
	}
	other, err := queue.Claim(t.Context(), time.Minute)
	if err != nil || other == nil || other.ID != secondID {
		t.Fatalf("other chat did not resume=%+v err=%v", other, err)
	}
}
