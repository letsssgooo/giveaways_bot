package delivery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/internal/testutil"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/notifications"
	deliveryworker "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/delivery"
)

type sendFunc func(context.Context, string, json.RawMessage) error

func (f sendFunc) Send(ctx context.Context, kind string, payload json.RawMessage) error {
	return f(ctx, kind, payload)
}

type fakeQueue struct {
	claim       func(context.Context, time.Duration) (*notifications.Message, error)
	sent        func(context.Context, int64, string) error
	retry       func(context.Context, int64, string, time.Duration, bool) error
	rateLimited func(context.Context, int64, string, time.Duration) error
}

func (q fakeQueue) Claim(ctx context.Context, lease time.Duration) (*notifications.Message, error) {
	return q.claim(ctx, lease)
}

func (q fakeQueue) Sent(ctx context.Context, id int64, token string) error {
	return q.sent(ctx, id, token)
}

func (q fakeQueue) Retry(ctx context.Context, id int64, token string, delay time.Duration, permanent bool) error {
	return q.retry(ctx, id, token, delay, permanent)
}

func (q fakeQueue) RateLimited(ctx context.Context, id int64, token string, delay time.Duration) error {
	return q.rateLimited(ctx, id, token, delay)
}

func queuedMessage() *notifications.Message {
	return &notifications.Message{ID: 7, Kind: "send_message", Payload: json.RawMessage(`{"chat_id":101,"text":"hello"}`), Attempts: 1, AttemptToken: "attempt-token"}
}

func TestDeliveryShutdownDrainsActiveSend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		intake, stop := context.WithCancel(t.Context())
		defer stop()
		started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		claims, sent := 0, false
		delivery := deliveryworker.Delivery{Logger: testutil.QuietLogger(), LeaseDuration: time.Minute,
			Sender: sendFunc(func(ctx context.Context, kind string, payload json.RawMessage) error {
				if kind != "send_message" || !json.Valid(payload) {
					t.Error("invalid queued payload")
				}
				close(started)
				select {
				case <-release:
					return ctx.Err()
				case <-ctx.Done():
					t.Error("intake shutdown canceled active delivery")
					return ctx.Err()
				}
			}),
			Queue: fakeQueue{
				claim: func(context.Context, time.Duration) (*notifications.Message, error) {
					claims++
					return queuedMessage(), nil
				},
				sent: func(ctx context.Context, id int64, token string) error {
					if ctx.Err() != nil || id != 7 || token != "attempt-token" {
						t.Error("sent result was not saved using a live context and lease token")
					}
					sent = true
					return nil
				},
			},
		}
		go func() { done <- delivery.Run(intake, t.Context()) }()
		<-started
		stop()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("Run returned before send completed: %v", err)
		default:
		}
		close(release)
		if err := <-done; err != nil || claims != 1 || !sent {
			t.Fatalf("Run()=%v claims=%d sent=%v", err, claims, sent)
		}
	})
}

func TestDeliveryHardStopLeavesLeaseForRecovery(t *testing.T) {
	intake, stop := context.WithCancel(t.Context())
	defer stop()
	work, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, done := make(chan struct{}), make(chan error, 1)
	delivery := deliveryworker.Delivery{Logger: testutil.QuietLogger(), LeaseDuration: time.Minute,
		Sender: sendFunc(func(ctx context.Context, _ string, _ json.RawMessage) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}),
		Queue: fakeQueue{
			claim: func(context.Context, time.Duration) (*notifications.Message, error) { return queuedMessage(), nil },
			sent: func(context.Context, int64, string) error {
				t.Error("hard-canceled attempt must keep its lease")
				return nil
			},
			retry: func(context.Context, int64, string, time.Duration, bool) error {
				t.Error("hard-canceled attempt must keep its lease")
				return nil
			},
		},
	}
	go func() { done <- delivery.Run(intake, work) }()
	<-started
	stop()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryClassifiesFailuresAndPersistsRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter int
		permanent  bool
		fatal      bool
		wantDelay  time.Duration
	}{
		{"bad request", 400, 0, true, false, time.Second},
		{"blocked by user", 403, 0, true, false, time.Second},
		{"unauthorized", 401, 0, false, true, time.Second},
		{"conflict", 409, 0, false, true, time.Second},
		{"rate limited", 429, 45, false, false, 45 * time.Second},
		{"rate limited without retry_after", 429, 0, false, false, time.Second},
		{"server unavailable", 503, 0, false, false, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := testutil.APIFailure(t, tc.status, tc.retryAfter)
			intake, stop := context.WithCancel(t.Context())
			defer stop()
			retried := false
			recordRetry := func(ctx context.Context, id int64, token string, delay time.Duration, permanent bool) error {
				if ctx.Err() != nil || id != 7 || token != "attempt-token" || delay != tc.wantDelay || permanent != tc.permanent {
					t.Fatalf("incorrect retry: id=%d token=%q delay=%s permanent=%v err=%v", id, token, delay, permanent, ctx.Err())
				}
				retried = true
				stop()
				return nil
			}
			delivery := deliveryworker.Delivery{Logger: testutil.QuietLogger(), LeaseDuration: time.Minute,
				Sender: sendFunc(func(context.Context, string, json.RawMessage) error { return failure }),
				Queue: fakeQueue{
					claim: func(context.Context, time.Duration) (*notifications.Message, error) { return queuedMessage(), nil },
					retry: func(ctx context.Context, id int64, token string, delay time.Duration, permanent bool) error {
						if tc.status == http.StatusTooManyRequests {
							t.Fatal("rate limit must persist a global pause")
						}
						return recordRetry(ctx, id, token, delay, permanent)
					},
					rateLimited: func(ctx context.Context, id int64, token string, delay time.Duration) error {
						if tc.status != http.StatusTooManyRequests {
							t.Fatal("non-rate-limit failure must not pause other recipients")
						}
						return recordRetry(ctx, id, token, delay, false)
					},
				},
			}
			if err := delivery.Run(intake, t.Context()); (err != nil) != tc.fatal || !retried {
				t.Fatalf("Run()=%v retried=%v; want fatal=%v", err, retried, tc.fatal)
			}
		})
	}
}

func TestDeliveryHonorsRateLimitBeforeClaimingAnotherMessage(t *testing.T) {
	failure := testutil.APIFailure(t, http.StatusTooManyRequests, 45)
	synctest.Test(t, func(t *testing.T) {
		intake, stop := context.WithCancel(t.Context())
		defer stop()
		claims := 0
		started := time.Now()
		delivery := deliveryworker.Delivery{Logger: testutil.QuietLogger(), LeaseDuration: time.Minute,
			Sender: sendFunc(func(context.Context, string, json.RawMessage) error { return failure }),
			Queue: fakeQueue{
				claim: func(context.Context, time.Duration) (*notifications.Message, error) {
					claims++
					if claims == 2 {
						if time.Since(started) < 45*time.Second {
							t.Fatal("another message was claimed before retry_after elapsed")
						}
						stop()
						return nil, context.Canceled
					}
					return queuedMessage(), nil
				},
				rateLimited: func(context.Context, int64, string, time.Duration) error { return nil },
			},
		}
		if err := delivery.Run(intake, t.Context()); err != nil || claims != 2 {
			t.Fatalf("Run()=%v claims=%d", err, claims)
		}
	})
}

func TestDeliveryContinuesAfterLeaseLost(t *testing.T) {
	intake, stop := context.WithCancel(t.Context())
	defer stop()
	claims := 0
	delivery := deliveryworker.Delivery{Logger: testutil.QuietLogger(), LeaseDuration: time.Minute,
		Sender: sendFunc(func(context.Context, string, json.RawMessage) error { return nil }),
		Queue: fakeQueue{
			claim: func(context.Context, time.Duration) (*notifications.Message, error) {
				claims++
				if claims == 2 {
					stop()
					return nil, context.Canceled
				}
				return queuedMessage(), nil
			},
			sent: func(context.Context, int64, string) error { return notifications.ErrLeaseLost },
		},
	}
	if err := delivery.Run(intake, t.Context()); err != nil || claims != 2 {
		t.Fatalf("Run()=%v claims=%d", err, claims)
	}
}

func TestDeliveryLimitsOutgoingRequestRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		intake, stop := context.WithCancel(t.Context())
		defer stop()
		var sentAt []time.Time
		delivery := deliveryworker.Delivery{Logger: testutil.QuietLogger(), LeaseDuration: time.Minute,
			Sender: sendFunc(func(context.Context, string, json.RawMessage) error {
				sentAt = append(sentAt, time.Now())
				return nil
			}),
			Queue: fakeQueue{
				claim: func(context.Context, time.Duration) (*notifications.Message, error) {
					return queuedMessage(), nil
				},
				sent: func(context.Context, int64, string) error {
					if len(sentAt) == 3 {
						stop()
					}
					return nil
				},
			},
		}
		if err := delivery.Run(intake, t.Context()); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(sentAt); i++ {
			if interval := sentAt[i].Sub(sentAt[i-1]); interval < 50*time.Millisecond {
				t.Errorf("requests were only %s apart", interval)
			}
		}
	})
}
