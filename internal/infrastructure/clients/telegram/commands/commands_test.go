package commands_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/testutil"
)

func TestSyncCommandsUsesPrivateChatScope(t *testing.T) {
	for _, admin := range []bool{true, false} {
		t.Run(fmt.Sprintf("admin=%t", admin), func(t *testing.T) {
			var requests atomic.Int32
			client := testutil.ClientFor(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				method := "deleteMyCommands"
				if admin {
					method = "setMyCommands"
				}
				if r.Method != http.MethodPost || r.URL.Path != "/bot"+testutil.Token+"/"+method {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
				}
				var scope struct {
					Type   string `json:"type"`
					ChatID int64  `json:"chat_id"`
				}
				if err := json.Unmarshal([]byte(r.FormValue("scope")), &scope); err != nil {
					t.Error(err)
				}
				if scope.Type != "chat" || scope.ChatID != 501 {
					t.Errorf("scope = %+v", scope)
				}
				if r.FormValue("language_code") != "" {
					t.Error("unexpected language override")
				}
				if admin {
					var commands []models.BotCommand
					if err := json.Unmarshal([]byte(r.FormValue("commands")), &commands); err != nil {
						t.Error(err)
					}
					var names []string
					for _, command := range commands {
						names = append(names, command.Command)
						if command.Description == "" {
							t.Errorf("missing description for %s", command.Command)
						}
					}
					if !slices.Equal(names, []string{"start", "menu", "help", "admin", "cancel", "prizes"}) {
						t.Errorf("commands = %v", names)
					}
				} else if commands := r.FormValue("commands"); commands != "" {
					t.Errorf("delete override includes commands %q", commands)
				}
				testutil.Respond(t, w, true)
			})
			if err := client.SyncCommands(t.Context(), 501, admin); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d", requests.Load())
			}
		})
	}
}

func TestSyncCommandsRejectsNonPrivateChatID(t *testing.T) {
	var requests atomic.Int32
	client := testutil.ClientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		testutil.Respond(t, w, true)
	})
	for _, chatID := range []int64{0, -10001} {
		for _, admin := range []bool{true, false} {
			err := client.SyncCommands(t.Context(), chatID, admin)
			if !telegram.IsPermanent(err) {
				t.Errorf("chat=%d admin=%t: error = %v", chatID, admin, err)
			}
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid chat IDs made %d requests", requests.Load())
	}
}

func TestSyncCommandsChecksConfirmationAndClassifiesErrors(t *testing.T) {
	for _, admin := range []bool{true, false} {
		for _, code := range []int{0, http.StatusBadRequest, http.StatusTooManyRequests} {
			t.Run(fmt.Sprintf("admin=%t/code=%d", admin, code), func(t *testing.T) {
				client := testutil.ClientFor(t, func(w http.ResponseWriter, _ *http.Request) {
					if code == 0 {
						testutil.Respond(t, w, false)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(code)
					if err := json.NewEncoder(w).Encode(map[string]any{
						"ok": false, "error_code": code, "description": testutil.Token,
						"parameters": map[string]int{"retry_after": 5},
					}); err != nil {
						t.Error(err)
					}
				})
				err := client.SyncCommands(t.Context(), 501, admin)
				if err == nil {
					t.Fatal("unconfirmed or failed operation succeeded")
				}
				method := "deleteMyCommands"
				if admin {
					method = "setMyCommands"
				}
				if !strings.Contains(err.Error(), method) || strings.Contains(err.Error(), testutil.Token) {
					t.Errorf("unsafe or incorrect method in error: %v", err)
				}
				if code == 0 && !strings.Contains(err.Error(), "operation was not confirmed") {
					t.Errorf("unconfirmed error = %v", err)
				}
				if telegram.IsPermanent(err) != (code == http.StatusBadRequest) || telegram.IsRateLimited(err) != (code == http.StatusTooManyRequests) {
					t.Errorf("incorrect error classification: %v", err)
				}
				if code == http.StatusTooManyRequests && telegram.RetryAfter(err) != 5*time.Second {
					t.Errorf("retry delay = %s", telegram.RetryAfter(err))
				}
			})
		}
	}
}
