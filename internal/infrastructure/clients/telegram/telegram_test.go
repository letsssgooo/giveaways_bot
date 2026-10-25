package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

const testToken = "12345:do-not-log-this-token"

func clientFor(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewWithBaseURL(testToken, time.Second, 2*time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func respond(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}); err != nil {
		t.Error(err)
	}
}

func TestIdentityAndConfiguration(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bot"+testToken+"/")
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		switch method {
		case "getMe":
			respond(t, w, models.User{ID: 12345, IsBot: true, Username: "giveaways_test_bot"})
		case "getWebhookInfo":
			respond(t, w, map[string]any{"url": "", "pending_update_count": 2})
		case "setMyDescription", "setMyCommands":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if method == "setMyDescription" && r.FormValue("description") == "" {
				t.Error("empty description")
			}
			if method == "setMyCommands" {
				var commands []models.BotCommand
				if err := json.Unmarshal([]byte(r.FormValue("commands")), &commands); err != nil {
					t.Error(err)
				}
				if len(commands) != 5 || commands[0].Command != "start" || commands[1].Command != "menu" || commands[2].Command != "help" || commands[3].Command != "cancel" || commands[4].Command != "prizes" {
					t.Errorf("commands = %+v", commands)
				}
				if scope := r.FormValue("scope"); scope != "" {
					t.Errorf("default commands have scope %q", scope)
				}
			}
			respond(t, w, true)
		default:
			t.Errorf("unexpected method %s", method)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	if len(methods) != 0 {
		t.Fatal("constructor made network requests")
	}
	me, err := client.GetMe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if me.Username != "giveaways_test_bot" {
		t.Fatalf("identity = %+v", me)
	}
	if err := client.Configure(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(methods, []string{"getMe", "getWebhookInfo", "setMyDescription", "setMyCommands"}) {
		t.Fatalf("methods = %v", methods)
	}
}

func TestRejectsOversizedTextAndInvalidPhotoBeforeSending(t *testing.T) {
	client := clientFor(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid saved payload reached Telegram")
	})
	for _, text := range []string{strings.Repeat("я", 4097), strings.Repeat("🎁", 2049)} {
		payload, err := json.Marshal(map[string]any{"chat_id": 501, "text": text})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Send(t.Context(), "send_message", payload); !IsPermanent(err) {
			t.Errorf("oversized text error: %v", err)
		}
	}
	for _, payload := range []string{`{"photo":"builtin:welcome"}`, `{"chat_id":501,"photo":"builtin:missing"}`, `{"chat_id":501,"photo":"file-id","caption":"unknown field"}`} {
		if err := client.Send(t.Context(), "send_photo", json.RawMessage(payload)); !IsPermanent(err) {
			t.Errorf("invalid photo error: %v", err)
		}
	}
}

func TestConfigurePreservesActiveWebhook(t *testing.T) {
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getWebhookInfo") {
			t.Errorf("unexpected mutation %s", r.URL.Path)
		}
		respond(t, w, map[string]any{"url": "https://example.invalid/secret-webhook"})
	})
	err := client.Configure(t.Context())
	if !IsFatal(err) || strings.Contains(err.Error(), "secret-webhook") {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchUsesCallerOffset(t *testing.T) {
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+testToken+"/getUpdates" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("unexpected request")
		}
		var params struct {
			Offset         int64    `json:"offset"`
			Limit          int      `json:"limit"`
			Timeout        int      `json:"timeout"`
			AllowedUpdates []string `json:"allowed_updates"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			t.Error(err)
		}
		if params.Offset != 42 || params.Limit != 100 || params.Timeout != 1 || !slices.Equal(params.AllowedUpdates, []string{"message", "callback_query"}) {
			t.Errorf("params = %+v", params)
		}
		respond(t, w, []models.Update{{ID: 42, Message: &models.Message{Text: "/start"}}, {ID: 43, CallbackQuery: &models.CallbackQuery{ID: "query"}}})
	})
	// Повтор после неудачного сохранения должен запросить тот же пакет.
	for range 2 {
		updates, err := client.Fetch(t.Context(), 42)
		if err != nil {
			t.Fatal(err)
		}
		if len(updates) != 2 || updates[0].ID != 42 || updates[1].CallbackQuery.ID != "query" {
			t.Fatalf("updates = %+v", updates)
		}
	}
	if _, err := client.Fetch(t.Context(), -1); !IsPermanent(err) {
		t.Fatalf("negative offset error = %v", err)
	}
}

func TestSendSavedPayloads(t *testing.T) {
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			if r.FormValue("chat_id") != "1234567890123" || r.FormValue("text") != "Привет" {
				t.Errorf("message = %v", r.Form)
			}
			var keyboard models.InlineKeyboardMarkup
			if err := json.Unmarshal([]byte(r.FormValue("reply_markup")), &keyboard); err != nil {
				t.Error(err)
			}
			if len(keyboard.InlineKeyboard) != 1 || len(keyboard.InlineKeyboard[0]) != 1 || keyboard.InlineKeyboard[0][0].CallbackData != "help" {
				t.Errorf("keyboard = %+v", keyboard)
			}
			respond(t, w, map[string]any{"message_id": 1})
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			if r.FormValue("callback_query_id") != "callback" || r.FormValue("text") != "Готово" {
				t.Errorf("callback = %v", r.Form)
			}
			respond(t, w, true)
		default:
			t.Error("unexpected request")
		}
	})
	if err := client.Send(t.Context(), "send_message", json.RawMessage(`{"chat_id":1234567890123,"text":"Привет","reply_markup":{"inline_keyboard":[[{"text":"Справка","callback_data":"help"}]]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(t.Context(), "answer_callback_query", json.RawMessage(`{"callback_query_id":"callback","text":"Готово"}`)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, payload string }{
		{"send_message", `{"chat_id":1}`},
		{"send_message", `{"chat_id":1,"text":"hello","unknown":true}`},
		{"send_message", `{"chat_id":1,"text":"hello"}{}`},
		{"answer_callback_query", `{"text":"hello"}`},
		{"unknown", `{}`},
	} {
		if err := client.Send(t.Context(), tc.kind, json.RawMessage(tc.payload)); !IsPermanent(err) {
			t.Errorf("invalid payload error = %v", err)
		}
	}
}

func TestErrorsAreClassifiedAndRedacted(t *testing.T) {
	for _, code := range []int{400, 401, 403, 409, 429, 500} {
		for _, status := range []int{http.StatusOK, code} {
			t.Run(fmt.Sprintf("code%d_status%d", code, status), func(t *testing.T) {
				client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(status)
					if err := json.NewEncoder(w).Encode(map[string]any{
						"ok": false, "error_code": code, "description": "secret " + testToken,
						"parameters": map[string]int{"retry_after": 7},
					}); err != nil {
						t.Error(err)
					}
				})
				calls := []func() error{
					func() error { _, err := client.Fetch(t.Context(), 0); return err },
					func() error { _, err := client.GetMe(t.Context()); return err },
					func() error {
						return client.Send(t.Context(), "send_message", json.RawMessage(`{"chat_id":1,"text":"hello"}`))
					},
				}
				for _, call := range calls {
					err := call()
					if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "secret ") {
						t.Fatalf("unsafe error = %v", err)
					}
					if IsPermanent(err) != (code == 400 || code == 403) {
						t.Errorf("permanent = %v for %v", IsPermanent(err), err)
					}
					if IsFatal(err) != (code == 401 || code == 409) {
						t.Errorf("fatal = %v for %v", IsFatal(err), err)
					}
					if IsRateLimited(fmt.Errorf("wrapped: %w", err)) != (code == http.StatusTooManyRequests) {
						t.Errorf("rate limited = %v for %v", IsRateLimited(err), err)
					}
					if code == 429 && RetryAfter(fmt.Errorf("wrapped: %w", err)) != 7*time.Second {
						t.Errorf("retry_after = %s", RetryAfter(err))
					}
				}
			})
		}
	}
}

func TestInvalidAndOversizedResponses(t *testing.T) {
	for _, body := range []string{testToken, `{"ok":true,"result":[{"update_id":0}]}`, `{"ok":true,"result":[]} trailing`, strings.Repeat("x", maxResponseSize+1)} {
		client := clientFor(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
		if _, err := client.Fetch(t.Context(), 0); err == nil || strings.Contains(err.Error(), testToken) {
			t.Fatalf("error = %v", err)
		}
	}
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, testToken) })
	if _, err := client.GetMe(t.Context()); err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("SDK error = %v", err)
	}
}

func TestContextCancellationAndNetworkErrors(t *testing.T) {
	release := make(chan struct{})
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Fetch(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch canceled: %v", err)
	}
	if _, err := client.GetMe(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("SDK canceled: %v", err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Fetch(ctx, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fetch deadline: %v", err)
	}
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("transport leaked " + testToken) })
	if _, err := client.GetMe(t.Context()); err == nil || strings.Contains(err.Error(), testToken) || IsPermanent(err) || IsFatal(err) {
		t.Fatalf("network error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRedirectDoesNotForwardToken(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed API redirect") }))
	defer target.Close()
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := client.Fetch(t.Context(), 0); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestCheckUsernameNetworkErrorHidesSecrets(t *testing.T) {
	client := clientFor(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected HTTP request") })
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private-details " + testToken)
	})
	got, err := client.CheckUsername(t.Context(), 501, "organizer")
	if got || err == nil || IsPermanent(err) || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "private-details") {
		t.Fatalf("network failure = %v, %v", got, err)
	}
}
