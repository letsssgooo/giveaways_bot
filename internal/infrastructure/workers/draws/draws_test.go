package draws

import (
	"context"
	"errors"
	io "io"
	slog "log/slog"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

func TestDrawSchedulerDrainsCurrentDrawAfterIntakeStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		intake, stop := context.WithCancel(t.Context())
		defer stop()
		started := make(chan context.Context, 1)
		release := make(chan struct{})
		calls := 0
		scheduler := DrawScheduler{Logger: quietLogger(), ProcessNext: func(ctx context.Context) (bool, error) {
			calls++
			started <- ctx
			<-release
			return true, nil
		}}
		done := make(chan error, 1)
		go func() { done <- scheduler.Run(intake, t.Context()) }()
		operationCtx := <-started
		stop()
		synctest.Wait()
		if operationCtx.Err() != nil {
			t.Fatal("stopping intake canceled the current draw")
		}
		select {
		case err := <-done:
			t.Fatalf("scheduler returned before completing the current draw: %v", err)
		default:
		}
		close(release)
		if err := <-done; err != nil || calls != 1 {
			t.Fatalf("Run()=%v calls=%d; want one completed draw", err, calls)
		}
	})
}

func TestDrawSchedulerWorkCancellationInterruptsCurrentDraw(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		work, cancelWork := context.WithCancel(t.Context())
		defer cancelWork()
		started := make(chan struct{})
		calls := 0
		scheduler := DrawScheduler{Logger: quietLogger(), ProcessNext: func(ctx context.Context) (bool, error) {
			calls++
			close(started)
			<-ctx.Done()
			return false, ctx.Err()
		}}
		done := make(chan error, 1)
		go func() { done <- scheduler.Run(t.Context(), work) }()
		<-started
		cancelWork()
		if err := <-done; err != nil || calls != 1 {
			t.Fatalf("Run()=%v calls=%d; canceled work must not retry", err, calls)
		}
	})
}

func TestDrawSchedulerLimitsEachOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		intake, stop := context.WithCancel(t.Context())
		defer stop()
		started := time.Now()
		var operationErr error
		scheduler := DrawScheduler{Logger: quietLogger(), ProcessNext: func(ctx context.Context) (bool, error) {
			<-ctx.Done()
			operationErr = ctx.Err()
			stop()
			return false, operationErr
		}}
		if err := scheduler.Run(intake, t.Context()); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(operationErr, context.DeadlineExceeded) || time.Since(started) != 30*time.Second {
			t.Fatalf("operation error=%v duration=%s; want a 30-second deadline", operationErr, time.Since(started))
		}
	})
}

func TestDrawSchedulerProcessesBacklogBeforeWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		intake, stop := context.WithCancel(t.Context())
		defer stop()
		started := time.Now()
		var attempts []time.Duration
		scheduler := DrawScheduler{Logger: quietLogger(), ProcessNext: func(context.Context) (bool, error) {
			attempts = append(attempts, time.Since(started))
			if len(attempts) == 5 {
				stop()
			}
			return len(attempts) <= 3, nil
		}}
		if err := scheduler.Run(intake, t.Context()); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(attempts, []time.Duration{0, 0, 0, 0, time.Second}) {
			t.Fatalf("attempt times=%v; backlog must run immediately, idle must wait one second", attempts)
		}
	})
}

func TestDrawSchedulerRetriesWithBackoffAndResetsAfterSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		intake, stop := context.WithCancel(t.Context())
		defer stop()
		started := time.Now()
		var attempts []time.Duration
		scheduler := DrawScheduler{Logger: quietLogger(), ProcessNext: func(context.Context) (bool, error) {
			attempts = append(attempts, time.Since(started))
			switch len(attempts) {
			case 3:
				return true, nil
			case 5:
				stop()
				return false, nil
			default:
				return false, errors.New("database unavailable")
			}
		}}
		if err := scheduler.Run(intake, t.Context()); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(attempts, []time.Duration{0, time.Second, 3 * time.Second, 3 * time.Second, 4 * time.Second}) {
			t.Fatalf("retry times=%v; expected backoff and reset after a successful draw", attempts)
		}
	})
}

func TestDrawSchedulerCancellationInterruptsRetryWait(t *testing.T) {
	for _, cancelIntake := range []bool{true, false} {
		name := "work"
		if cancelIntake {
			name = "intake"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				intake, stop := context.WithCancel(t.Context())
				defer stop()
				work, cancelWork := context.WithCancel(t.Context())
				defer cancelWork()
				started := time.Now()
				calls := 0
				scheduler := DrawScheduler{Logger: quietLogger(), ProcessNext: func(context.Context) (bool, error) {
					calls++
					return false, errors.New("database unavailable")
				}}
				done := make(chan error, 1)
				go func() { done <- scheduler.Run(intake, work) }()
				synctest.Wait()
				if cancelIntake {
					stop()
				} else {
					cancelWork()
				}
				if err := <-done; err != nil || calls != 1 || time.Since(started) != 0 {
					t.Fatalf("Run()=%v calls=%d duration=%s; cancellation must interrupt backoff", err, calls, time.Since(started))
				}
			})
		})
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
