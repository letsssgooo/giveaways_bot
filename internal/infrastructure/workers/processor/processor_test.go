package processor_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/internal/testutil"

	processorworker "github.com/letsssgooo/giveaways_bot/internal/infrastructure/workers/processor"
)

func TestProcessorDrainsActiveTransaction(t *testing.T) {
	intake, stop := context.WithCancel(t.Context())
	defer stop()
	calls := 0
	processor := processorworker.Processor{Logger: testutil.QuietLogger(), ProcessNext: func(ctx context.Context) (bool, error) {
		calls++
		stop()
		if ctx.Err() != nil {
			t.Fatal("intake cancellation interrupted an active transaction")
		}
		return true, nil
	}}
	if err := processor.Run(intake, t.Context()); err != nil || calls != 1 {
		t.Fatalf("Run()=%v calls=%d", err, calls)
	}
}

func TestProcessorRetriesTransactionAfterFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		intake, stop := context.WithCancel(t.Context())
		defer stop()
		calls := 0
		started := time.Now()
		processor := processorworker.Processor{Logger: testutil.QuietLogger(), ProcessNext: func(context.Context) (bool, error) {
			calls++
			if calls == 1 {
				return false, errors.New("transaction rolled back")
			}
			stop()
			return true, nil
		}}
		if err := processor.Run(intake, t.Context()); err != nil || calls != 2 || time.Since(started) != time.Second {
			t.Fatalf("Run()=%v calls=%d elapsed=%s", err, calls, time.Since(started))
		}
	})
}
