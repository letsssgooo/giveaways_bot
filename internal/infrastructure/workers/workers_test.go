package workers_test

import (
	"testing"
	"time"

	workerbase "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers"
)

func TestBackoff(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{{-1, time.Second}, {0, time.Second}, {1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second}, {4, 8 * time.Second}, {5, 16 * time.Second}, {6, 30 * time.Second}, {100000, 30 * time.Second}} {
		if got := workerbase.Backoff(tc.attempt); got != tc.want {
			t.Fatalf("backoff(%d) = %s; want %s", tc.attempt, got, tc.want)
		}
	}
}
