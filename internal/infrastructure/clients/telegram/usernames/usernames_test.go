package usernames_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/testutil"
)

func TestCheckUsernameUsesCurrentPrivateChat(t *testing.T) {
	for _, tc := range []struct {
		name     string
		username string
		want     bool
	}{
		{"match", "organizer", true},
		{"case insensitive", "OrgAnizer", true},
		{"changed", "another_organizer", false},
		{"removed", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testutil.ClientFor(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/getChat") {
					t.Error("unexpected username verification request")
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
				}
				if got := r.FormValue("chat_id"); got != "501" {
					t.Errorf("chat_id = %q; want numeric user ID", got)
				}
				testutil.Respond(t, w, models.ChatFullInfo{ID: 501, Type: models.ChatTypePrivate, Username: tc.username})
			})
			got, err := client.CheckUsername(t.Context(), 501, "organizer")
			if err != nil || got != tc.want {
				t.Fatalf("CheckUsername = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestCheckUsernameRejectsInvalidIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result any
	}{
		{"null", nil},
		{"missing ID", models.ChatFullInfo{Type: models.ChatTypePrivate, Username: "organizer"}},
		{"different ID", models.ChatFullInfo{ID: 502, Type: models.ChatTypePrivate, Username: "organizer"}},
		{"group", models.ChatFullInfo{ID: 501, Type: models.ChatTypeGroup, Username: "organizer"}},
		{"missing type", models.ChatFullInfo{ID: 501, Username: "organizer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testutil.ClientFor(t, func(w http.ResponseWriter, _ *http.Request) { testutil.Respond(t, w, tc.result) })
			if got, err := client.CheckUsername(t.Context(), 501, "organizer"); got || err == nil || telegram.IsPermanent(err) {
				t.Fatalf("invalid identity = %v, %v", got, err)
			}
		})
	}
}

func TestCheckUsernameAPIErrors(t *testing.T) {
	for _, code := range []int{400, 401, 403, 429, 500} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			client := testutil.ClientFor(t, func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(map[string]any{
					"ok": false, "error_code": code, "description": "private-details " + testutil.Token,
					"parameters": map[string]any{"retry_after": 17},
				}); err != nil {
					t.Error(err)
				}
			})
			got, err := client.CheckUsername(t.Context(), 501, "organizer")
			if got {
				t.Fatal("API failure confirmed username")
			}
			if code == 400 || code == 403 {
				if err != nil {
					t.Fatalf("unavailable private chat = %v; want no match", err)
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), testutil.Token) || strings.Contains(err.Error(), "private-details") {
				t.Fatalf("unsafe or missing API error: %v", err)
			}
			if code == 429 && telegram.RetryAfter(err) != 17*time.Second {
				t.Fatalf("telegram.RetryAfter = %s", telegram.RetryAfter(err))
			}
		})
	}
}

func TestCheckUsernameMalformedResponseHidesSecrets(t *testing.T) {
	client := testutil.ClientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("private-details " + testutil.Token))
	})
	got, err := client.CheckUsername(t.Context(), 501, "organizer")
	if got || err == nil || strings.Contains(err.Error(), testutil.Token) || strings.Contains(err.Error(), "private-details") {
		t.Fatalf("malformed response = %v, %v", got, err)
	}
}

func TestCheckUsernameCancellation(t *testing.T) {
	client := testutil.ClientFor(t, func(http.ResponseWriter, *http.Request) {
		t.Error("canceled verification reached Telegram")
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := client.CheckUsername(ctx, 501, "organizer")
	if got || !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), testutil.Token) {
		t.Fatalf("canceled verification = %v, %v", got, err)
	}
}

func TestCheckUsernameRejectsInvalidInputWithoutRequest(t *testing.T) {
	client := testutil.ClientFor(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid input reached Telegram")
	})
	for _, tc := range []struct {
		id       int64
		username string
	}{{0, "organizer"}, {-501, "organizer"}, {501, ""}} {
		if got, err := client.CheckUsername(t.Context(), tc.id, tc.username); got || !telegram.IsPermanent(err) {
			t.Fatalf("invalid input accepted: %v, %v", got, err)
		}
	}
}
