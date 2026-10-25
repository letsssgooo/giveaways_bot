//go:build integration

package tests_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/letsssgooo/giveaways_bot/internal/app"
	telegramclient "github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/tests/utils"
)

// Both paths run the real composition root, all five workers, PostgreSQL and
// Telegram's HTTP protocol. Only Telegram is replaced by a local HTTP server.
func TestE2EGiveawayAndClaimAfterRestart(t *testing.T) {
	for _, reserve := range []bool{false, true} {
		name := "winner"
		if reserve {
			name = "reserve_after_deadline"
		}
		t.Run(name, func(t *testing.T) {
			db := e2eDatabase(t)
			utils.Exec(t, db, `
				INSERT INTO promotions(name,starts_on,ends_on,consent_version,consent_text)
				SELECT 'E2E',(clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date,
					(clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date,'e2e-v1','Consent to share names';
				INSERT INTO consent_versions(version,consent_text) VALUES ('e2e-v1','Consent to share names');
				INSERT INTO partners(name,telegram_chat_id,subscription_url) VALUES ('E2E partner',-700,'https://t.me/partner');
				INSERT INTO partner_days(partner_id,date,opens_at,closes_at)
				SELECT 1,(clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date,
					date_trunc('day',clock_timestamp() AT TIME ZONE 'Europe/Moscow') AT TIME ZONE 'Europe/Moscow',
					(date_trunc('day',clock_timestamp() AT TIME ZONE 'Europe/Moscow')+interval '1 day') AT TIME ZONE 'Europe/Moscow';
				INSERT INTO promo_codes(partner_id,code) VALUES (1,'E2E-1'),(1,'E2E-2');
				INSERT INTO draws(kind,partner_day_id,scheduled_at,reserve_count)
				SELECT 'daily',id,closes_at,1 FROM partner_days;
				INSERT INTO prizes(draw_id,name,instructions,places_count,units_per_winner)
				VALUES (1,'Festival ticket','Contact organizer',1,1);
				UPDATE administration_settings SET winner_response_hours=1,response_starts_on='delivered',undeliverable_policy='reserve';`)
			api := newE2ETelegram(t)
			stop := e2eRun(t, db, api)
			for _, user := range []int64{1001, 1002} {
				e2eProcessed(t, db, api.message(user, "/start"))
				e2eProcessed(t, db, api.press(t, user, "menu:code"))
				e2eProcessed(t, db, api.press(t, user, "partner:join:1"))
				e2eProcessed(t, db, api.press(t, user, "registration:check:1"))
			}
			e2eProcessed(t, db, api.message(9001, "/start"))
			e2eAssertCount(t, db, `SELECT count(*) FROM registrations`, 2)
			e2eAssertCount(t, db, `SELECT count(*) FROM promo_codes WHERE user_id IS NOT NULL`, 2)
			e2eAssertCount(t, db, `SELECT count(*) FROM active_days`, 2)
			// Advance the database's test clock; the real scheduler discovers the
			// due draw. No direct call to execution/approval/claim bypasses wiring.
			e2eAdvance(t, db, 24*time.Hour)
			e2eWait(t, db, "scheduled draw", `SELECT performed_at IS NOT NULL FROM draws WHERE id=1`)
			e2eAssertCount(t, db, `SELECT count(*) FROM prize_awards WHERE notice_queued_at IS NOT NULL OR notified_at IS NOT NULL`, 0)
			e2eAssertCount(t, db, `SELECT count(*) FROM outbox_messages WHERE prize_award_id IS NOT NULL`, 0)
			e2eProcessed(t, db, api.callback(9001, "draw:approve:1"))
			e2eProcessed(t, db, api.callback(9001, "draw:confirm:1"))
			e2eWait(t, db, "winner notification delivered", `SELECT EXISTS(SELECT 1 FROM prize_awards WHERE notified_at IS NOT NULL)`)
			var awardID, recipient int64
			if err := db.QueryRow(t.Context(), `SELECT a.id,u.telegram_user_id FROM prize_awards a
				JOIN draw_results r ON r.id=a.draw_result_id JOIN draw_participants p ON p.id=r.draw_participant_id
				JOIN users u ON u.id=p.user_id WHERE a.is_current`).Scan(&awardID, &recipient); err != nil {
				t.Fatal(err)
			}
			initialAward, initialRecipient := awardID, recipient
			if reserve {
				e2eAdvance(t, db, 2*time.Hour)
				e2eWait(t, db, "reserve assigned and notified", `SELECT EXISTS(SELECT 1 FROM prize_awards WHERE previous_award_id IS NOT NULL AND is_current AND notified_at IS NOT NULL)`)
				if err := db.QueryRow(t.Context(), `SELECT a.id,u.telegram_user_id FROM prize_awards a
					JOIN draw_results r ON r.id=a.draw_result_id JOIN draw_participants p ON p.id=r.draw_participant_id
					JOIN users u ON u.id=p.user_id WHERE a.is_current`).Scan(&awardID, &recipient); err != nil {
					t.Fatal(err)
				}
				if awardID == initialAward || recipient == initialRecipient {
					t.Fatal("reserve reused original winner")
				}
				e2eProcessed(t, db, api.callback(initialRecipient, fmt.Sprintf("prize:claim:%d", initialAward)))
				e2eAssertCount(t, db, `SELECT count(*) FROM dialogs WHERE state LIKE 'prize:%'`, 0)
			}
			nonce := api.press(t, recipient, fmt.Sprintf("prize:claim:%d", awardID))
			e2eProcessed(t, db, nonce)
			// Plain text never substitutes for pressing the explicit consent button.
			e2eProcessed(t, db, api.message(recipient, "Anna"))
			e2eAssertCount(t, db, `SELECT count(*) FROM prize_awards WHERE consent_given_at IS NOT NULL OR first_name IS NOT NULL`, 0)
			e2eProcessed(t, db, api.press(t, recipient, fmt.Sprintf("prize:consent:%d:%d", awardID, nonce)))
			e2eProcessed(t, db, api.message(recipient, "Анна"))
			e2eWait(t, db, "first name persisted as draft", `SELECT EXISTS(SELECT 1 FROM dialogs WHERE state='prize:last_name' AND payload->>'first_name'='Анна')`)
			stop()
			stop = e2eRun(t, db, api)
			confirmation := api.message(recipient, "Иванова")
			e2eProcessed(t, db, confirmation)
			e2eAssertCount(t, db, `SELECT count(*) FROM prize_awards WHERE confirmed_at IS NOT NULL OR first_name IS NOT NULL`, 0)
			e2eProcessed(t, db, api.press(t, recipient, fmt.Sprintf("prize:confirm:%d:%d", awardID, confirmation)))
			e2eWait(t, db, "claim confirmed", `SELECT EXISTS(SELECT 1 FROM prize_awards WHERE is_current AND confirmed_at IS NOT NULL
				AND first_name='Анна' AND last_name='Иванова' AND consent_version='e2e-v1')`)
			// Replay a previously acknowledged Telegram update after restart.
			api.replay(confirmation)
			e2eProcessed(t, db, api.callback(recipient, fmt.Sprintf("prize:confirm:%d:%d", awardID, confirmation)))
			e2eWait(t, db, "all outbound messages", `SELECT NOT EXISTS(SELECT 1 FROM outbox_messages WHERE status IN ('pending','processing'))`)
			stop()
			e2eAssertCount(t, db, `SELECT count(*) FROM outbox_messages WHERE status='failed'`, 0)
			e2eAssertCount(t, db, `SELECT count(*) FROM prize_awards WHERE confirmed_at IS NOT NULL`, 1)
			e2eAssertCount(t, db, `SELECT count(*) FROM registrations`, 2)
			e2eAssertCount(t, db, `SELECT count(*) FROM background_jobs WHERE kind='notify_draw_winners'`, 1)
			e2eAssertCount(t, db, `SELECT count(*) FROM dialogs WHERE state LIKE 'prize:%'`, 0)
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.calls["getChatMember"] != 4 || api.calls["sendPhoto"] != 3 || api.calls["sendMessage"] == 0 || api.calls["answerCallbackQuery"] == 0 {
				t.Fatalf("missing real HTTP flow: %v", api.calls)
			}
		})
	}
}

func e2eDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := utils.NewDB(t)
	// The clock runs normally, with an adjustable offset. This keeps delivery
	// throttling realistic while allowing days/deadlines to pass immediately.
	now := time.Now().UTC()
	future := now.Truncate(24 * time.Hour).Add(36 * time.Hour)
	utils.Exec(t, db, `CREATE TABLE e2e_clock(delta interval NOT NULL)`)
	utils.Exec(t, db, `INSERT INTO e2e_clock VALUES ($1 * interval '1 microsecond')`, future.Sub(now).Microseconds())
	utils.Exec(t, db, `CREATE FUNCTION clock_timestamp() RETURNS timestamptz LANGUAGE SQL VOLATILE
		RETURN (SELECT pg_catalog.clock_timestamp()+delta FROM e2e_clock)`)
	cfg := db.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] += ",pg_catalog"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func e2eAdvance(t *testing.T, db *pgxpool.Pool, delta time.Duration) {
	t.Helper()
	utils.Exec(t, db, `UPDATE e2e_clock SET delta=delta+$1*interval '1 microsecond'`, delta.Microseconds())
}

func e2eRun(t *testing.T, db *pgxpool.Pool, api *e2eTelegram) func() {
	t.Helper()
	client, err := telegramclient.NewWithBaseURL("123:integration", time.Second, 3*time.Second, api.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	a := app.App{DatabaseURL: utils.DBURL(db), Telegram: client, ShutdownTimeout: 5 * time.Second, RequestTimeout: 3 * time.Second,
		AdminTelegramIDs: []int64{9001}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go func() { done <- a.Run(ctx) }()
	var once sync.Once
	stop := func() {
		t.Helper()
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("App.Run: %v", err)
				}
			case <-time.After(8 * time.Second):
				t.Error("App.Run failed to shut down")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func e2eProcessed(t *testing.T, db *pgxpool.Pool, id int64) {
	t.Helper()
	e2eWait(t, db, fmt.Sprintf("update %d", id), `SELECT EXISTS(SELECT 1 FROM telegram_updates WHERE update_id=$1 AND processed_at IS NOT NULL)`, id)
}

func e2eWait(t *testing.T, db *pgxpool.Pool, label, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var ok bool
		err := db.QueryRow(ctx, query, args...).Scan(&ok)
		if err == nil && ok {
			return
		}
		if err != nil {
			t.Fatalf("waiting for %s: %v", label, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", label)
		case <-ticker.C:
		}
	}
}

func e2eAssertCount(t *testing.T, db *pgxpool.Pool, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(t.Context(), query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s: got %d want %d", query, got, want)
	}
}

type e2eTelegram struct {
	mu      sync.Mutex
	server  *httptest.Server
	updates []models.Update
	forced  []models.Update
	calls   map[string]int
	nextID  int64
	notify  chan struct{}
	sent    []e2eSent
}

type e2eSent struct {
	chatID int64
	markup models.InlineKeyboardMarkup
}

func newE2ETelegram(t *testing.T) *e2eTelegram {
	t.Helper()
	api := &e2eTelegram{calls: make(map[string]int), notify: make(chan struct{}, 1)}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		api.mu.Lock()
		api.calls[method]++
		api.mu.Unlock()
		respond := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
		}
		switch method {
		case "getMe":
			respond(map[string]any{"id": 123, "is_bot": true, "first_name": "Test", "username": "integration_bot"})
		case "getWebhookInfo":
			respond(map[string]any{"url": ""})
		case "setMyDescription", "setMyCommands", "deleteMyCommands", "answerCallbackQuery":
			respond(true)
		case "getChatMember":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			userID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
			if err != nil {
				t.Error(err)
				return
			}
			status := "member"
			if userID == 123 {
				status = "administrator"
			}
			respond(map[string]any{"status": status, "user": map[string]any{"id": userID, "is_bot": userID == 123, "first_name": "User"}})
		case "sendMessage", "sendPhoto":
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			chatID, err := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
			if err != nil || chatID <= 0 {
				t.Errorf("invalid outgoing chat: %q", r.FormValue("chat_id"))
				return
			}
			if method == "sendMessage" && r.FormValue("text") == "" {
				t.Error("empty outgoing message")
				return
			}
			var markup models.InlineKeyboardMarkup
			if value := r.FormValue("reply_markup"); value != "" {
				if err := json.Unmarshal([]byte(value), &markup); err != nil {
					t.Error(err)
					return
				}
			}
			api.mu.Lock()
			api.sent = append(api.sent, e2eSent{chatID, markup})
			api.mu.Unlock()
			respond(map[string]any{"message_id": 1, "date": 1, "chat": map[string]any{"id": chatID, "type": "private"}})
		case "getUpdates":
			var request struct {
				Offset int64 `json:"offset"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("getUpdates body: %v", err)
				return
			}
			read := func() []models.Update {
				api.mu.Lock()
				defer api.mu.Unlock()
				result := append([]models.Update{}, api.forced...)
				api.forced = nil
				for _, update := range api.updates {
					if update.ID >= request.Offset {
						result = append(result, update)
					}
				}
				return result
			}
			updates := read()
			if len(updates) == 0 {
				timer := time.NewTimer(100 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-api.notify:
				case <-timer.C:
				case <-r.Context().Done():
					return
				}
				updates = read()
			}
			respond(updates)
		default:
			t.Errorf("unexpected Telegram method: %s", method)
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (a *e2eTelegram) message(user int64, text string) int64 {
	return a.push(models.Update{Message: &models.Message{ID: 1, Date: 1, Text: text, From: &models.User{ID: user, FirstName: "User"}, Chat: models.Chat{ID: user, Type: models.ChatTypePrivate}}})
}
func (a *e2eTelegram) callback(user int64, data string) int64 {
	return a.push(models.Update{CallbackQuery: &models.CallbackQuery{ID: fmt.Sprintf("%d:%s", user, data), From: models.User{ID: user, FirstName: "User"}, Data: data,
		Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage, Message: &models.Message{ID: 1, Date: 1, Chat: models.Chat{ID: user, Type: models.ChatTypePrivate}}}}})
}

// Click only buttons actually delivered to this user's chat. An incorrect
// recipient or callback payload must fail the smoke test as well as SQL errors.
func (a *e2eTelegram) press(t *testing.T, user int64, data string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		found := false
		a.mu.Lock()
		for _, message := range a.sent {
			if message.chatID != user {
				continue
			}
			for _, row := range message.markup.InlineKeyboard {
				for _, button := range row {
					if button.CallbackData == data {
						found = true
					}
				}
			}
		}
		a.mu.Unlock()
		if found {
			return a.callback(user, data)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("button %s was never delivered to chat %d", data, user)
		case <-ticker.C:
		}
	}
}
func (a *e2eTelegram) push(update models.Update) int64 {
	a.mu.Lock()
	a.nextID++
	update.ID = a.nextID
	a.updates = append(a.updates, update)
	a.mu.Unlock()
	select {
	case a.notify <- struct{}{}:
	default:
	}
	return update.ID
}
func (a *e2eTelegram) replay(id int64) {
	a.mu.Lock()
	for _, update := range a.updates {
		if update.ID == id {
			a.forced = append(a.forced, update)
			break
		}
	}
	a.mu.Unlock()
	select {
	case a.notify <- struct{}{}:
	default:
	}
}
