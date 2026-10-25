package subscription_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/testutil"
)

func TestCheckSubscriptionMembership(t *testing.T) {
	for _, tc := range []struct {
		status   string
		isMember bool
		want     bool
	}{
		{"creator", false, true},
		{"administrator", false, true},
		{"member", false, true},
		{"restricted", true, true},
		{"restricted", false, false},
		{"left", false, false},
		{"kicked", false, false},
	} {
		t.Run(tc.status+"/"+strconv.FormatBool(tc.isMember), func(t *testing.T) {
			requests := make(chan int64, 2)
			client := testutil.ClientFor(t, func(w http.ResponseWriter, r *http.Request) {
				userID := subscriptionRequestUser(t, r)
				requests <- userID
				status, isMember := tc.status, tc.isMember
				if userID == 12345 {
					status = "administrator"
				}
				testutil.Respond(t, w, memberResult(status, userID, isMember))
			})
			got, err := client.CheckSubscription(t.Context(), -100123, 501)
			if err != nil || got != tc.want {
				t.Fatalf("CheckSubscription = %v, %v; want %v", got, err, tc.want)
			}
			if len(requests) != 2 || <-requests != 12345 || <-requests != 501 {
				t.Fatal("expected bot permissions check before user membership")
			}
		})
	}
}

func TestCheckSubscriptionRequiresBotAdministrator(t *testing.T) {
	for _, status := range []string{"creator", "administrator", "member", "restricted", "left", "kicked"} {
		t.Run(status, func(t *testing.T) {
			var requests atomic.Int32
			client := testutil.ClientFor(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				userID := subscriptionRequestUser(t, r)
				responseStatus := status
				if userID == 501 {
					responseStatus = "member"
				}
				testutil.Respond(t, w, memberResult(responseStatus, userID, true))
			})
			got, err := client.CheckSubscription(t.Context(), -100123, 501)
			if status == "creator" || status == "administrator" {
				if err != nil || !got || requests.Load() != 2 {
					t.Fatalf("authorized check = %v, %v; requests = %d", got, err, requests.Load())
				}
			} else if err == nil || got || requests.Load() != 1 || !strings.Contains(err.Error(), "administrator") {
				t.Fatalf("missing rights check = %v, %v; requests = %d", got, err, requests.Load())
			}
		})
	}
}

func TestCheckSubscriptionRejectsInvalidResponses(t *testing.T) {
	for _, result := range []any{
		nil,
		map[string]any{"status": "future_status", "user": map[string]any{"id": 501}},
		map[string]any{"status": "member"},
		memberResult("member", 999, false),
	} {
		for _, failAt := range []int32{1, 2} {
			name, _ := json.Marshal(result)
			t.Run(strconv.Itoa(int(failAt))+"/"+string(name), func(t *testing.T) {
				var requests atomic.Int32
				client := testutil.ClientFor(t, func(w http.ResponseWriter, r *http.Request) {
					userID := subscriptionRequestUser(t, r)
					if requests.Add(1) == failAt {
						testutil.Respond(t, w, result)
						return
					}
					testutil.Respond(t, w, memberResult("administrator", userID, true))
				})
				got, err := client.CheckSubscription(t.Context(), -100123, 501)
				if err == nil || got || requests.Load() != failAt {
					t.Fatalf("invalid response = %v, %v; requests = %d", got, err, requests.Load())
				}
			})
		}
	}
}

func TestCheckSubscriptionAPIErrorsRemainErrorsAndHideSecrets(t *testing.T) {
	for _, code := range []int{400, 403, 429, 500} {
		for _, failAt := range []int32{1, 2} {
			t.Run(strconv.Itoa(code)+"/"+strconv.Itoa(int(failAt)), func(t *testing.T) {
				var requests atomic.Int32
				client := testutil.ClientFor(t, func(w http.ResponseWriter, r *http.Request) {
					userID := subscriptionRequestUser(t, r)
					if requests.Add(1) != failAt {
						testutil.Respond(t, w, memberResult("administrator", userID, true))
						return
					}
					if err := json.NewEncoder(w).Encode(map[string]any{
						"ok": false, "error_code": code, "description": "private-details " + testutil.Token,
						"parameters": map[string]any{"retry_after": 17},
					}); err != nil {
						t.Error(err)
					}
				})
				got, err := client.CheckSubscription(t.Context(), -100123, 501)
				if err == nil || got || requests.Load() != failAt {
					t.Fatalf("API failure = %v, %v; requests = %d", got, err, requests.Load())
				}
				if strings.Contains(err.Error(), testutil.Token) || strings.Contains(err.Error(), "private-details") {
					t.Fatal("error contains API secrets")
				}
				if code == 429 && telegram.RetryAfter(err) != 17*time.Second {
					t.Fatalf("telegram.RetryAfter = %s", telegram.RetryAfter(err))
				}
			})
		}
	}
}

func TestCheckSubscriptionCancellation(t *testing.T) {
	started := make(chan struct{})
	client := testutil.ClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		subscriptionRequestUser(t, r)
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := client.CheckSubscription(ctx, -100123, 501)
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("subscription request did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("subscription check ignored cancellation")
	}
}

func TestCheckSubscriptionRejectsInvalidIDsWithoutRequest(t *testing.T) {
	client := testutil.ClientFor(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid subscription IDs reached Telegram")
	})
	for _, ids := range [][2]int64{{0, 501}, {100123, 501}, {-100123, 0}, {-100123, -501}} {
		if got, err := client.CheckSubscription(t.Context(), ids[0], ids[1]); err == nil || got {
			t.Fatalf("invalid IDs accepted: %v, %v", got, err)
		}
	}
}

func subscriptionRequestUser(t *testing.T, r *http.Request) int64 {
	t.Helper()
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/getChatMember") {
		t.Error("unexpected membership request")
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Error(err)
	}
	if r.FormValue("chat_id") != "-100123" {
		t.Errorf("chat_id = %q", r.FormValue("chat_id"))
	}
	userID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil {
		t.Error(err)
	}
	return userID
}

func memberResult(status string, userID int64, isMember bool) map[string]any {
	return map[string]any{"status": status, "user": map[string]any{"id": userID}, "is_member": isMember}
}
