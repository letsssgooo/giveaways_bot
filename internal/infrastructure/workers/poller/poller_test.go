package poller_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/internal/testutil"

	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/updates"
	pollerworker "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/poller"
)

type fetchFunc func(context.Context, int64) ([]models.Update, error)

func (f fetchFunc) Fetch(ctx context.Context, offset int64) ([]models.Update, error) {
	return f(ctx, offset)
}

type fakeUpdateStore struct {
	offset func(context.Context) (int64, error)
	save   func(context.Context, int64, []updates.Event) error
}

func (s fakeUpdateStore) Offset(ctx context.Context) (int64, error) { return s.offset(ctx) }

func (s fakeUpdateStore) Save(ctx context.Context, offset int64, events []updates.Event) error {
	return s.save(ctx, offset, events)
}

func TestPollerPersistsReceivedBatchDuringShutdown(t *testing.T) {
	intake, stop := context.WithCancel(t.Context())
	defer stop()
	fetched := false
	saved := false
	poller := pollerworker.Poller{
		Logger: testutil.QuietLogger(),
		Fetcher: fetchFunc(func(ctx context.Context, offset int64) ([]models.Update, error) {
			if offset != 11 || ctx != intake {
				t.Fatal("incorrect polling context or offset")
			}
			fetched = true
			stop()
			return []models.Update{{ID: 11}, {ID: 12}}, nil
		}),
		Store: fakeUpdateStore{
			offset: func(context.Context) (int64, error) { return 11, nil },
			save: func(ctx context.Context, offset int64, events []updates.Event) error {
				if ctx.Err() != nil || offset != 11 || !fetched || len(events) != 2 || events[0].ID != 11 || events[1].ID != 12 {
					t.Fatal("received batch was not saved using the live work context")
				}
				var update models.Update
				if err := json.Unmarshal(events[1].Payload, &update); err != nil || update.ID != 12 {
					t.Fatalf("incorrect saved payload: %s (%v)", events[1].Payload, err)
				}
				saved = true
				return nil
			},
		},
	}
	if err := poller.Run(intake, t.Context()); err != nil || !saved {
		t.Fatalf("Run() = %v, saved=%v", err, saved)
	}
}

func TestPollerDoesNotAcknowledgeBeforePersistence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		intake, stop := context.WithCancel(t.Context())
		defer stop()
		var offset int64
		fetches, saves := 0, 0
		poller := pollerworker.Poller{
			Logger: testutil.QuietLogger(),
			Fetcher: fetchFunc(func(_ context.Context, got int64) ([]models.Update, error) {
				fetches++
				if fetches == 1 {
					if got != 0 {
						t.Fatalf("initial offset=%d", got)
					}
					return []models.Update{{ID: 10}}, nil
				}
				if got != 11 || saves != 2 {
					t.Fatalf("batch acknowledged before persistence: offset=%d saves=%d", got, saves)
				}
				stop()
				return nil, context.Canceled
			}),
			Store: fakeUpdateStore{
				offset: func(context.Context) (int64, error) { return offset, nil },
				save: func(_ context.Context, fetchedOffset int64, events []updates.Event) error {
					saves++
					if fetchedOffset != 0 || fetches != 1 || len(events) != 1 || events[0].ID != 10 {
						t.Fatal("retried batch changed or another request acknowledged it")
					}
					if saves == 1 {
						return errors.New("database unavailable")
					}
					offset = 11
					return nil
				},
			},
		}
		if err := poller.Run(intake, t.Context()); err != nil || fetches != 2 || saves != 2 {
			t.Fatalf("Run()=%v fetches=%d saves=%d", err, fetches, saves)
		}
	})
}

func TestEmptyPollDoesNotSpin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		intake, stop := context.WithTimeout(t.Context(), 550*time.Millisecond)
		defer stop()
		calls := 0
		poller := pollerworker.Poller{Logger: testutil.QuietLogger(),
			Fetcher: fetchFunc(func(context.Context, int64) ([]models.Update, error) { calls++; return nil, nil }),
			Store:   fakeUpdateStore{offset: func(context.Context) (int64, error) { return 0, nil }},
		}
		if err := poller.Run(intake, t.Context()); err != nil || calls != 6 {
			t.Fatalf("Run()=%v calls=%d; want 6 requests in 550ms", err, calls)
		}
	})
}

func TestPollerStopsOnFatalTelegramError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusConflict} {
		failure := testutil.APIFailure(t, status, 0)
		poller := pollerworker.Poller{Logger: testutil.QuietLogger(),
			Fetcher: fetchFunc(func(context.Context, int64) ([]models.Update, error) { return nil, failure }),
			Store:   fakeUpdateStore{offset: func(context.Context) (int64, error) { return 0, nil }},
		}
		if err := poller.Run(t.Context(), t.Context()); err == nil {
			t.Fatalf("Run should fail for HTTP %d", status)
		}
	}
}
