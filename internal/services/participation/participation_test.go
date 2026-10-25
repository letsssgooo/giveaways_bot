package participation

import (
	"context"
	"errors"
	"testing"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/participation"
)

type codeRepositoryStub struct {
	options func(context.Context, int64) ([]entity.CodeOption, error)
	request func(context.Context, int64, int64, int64) (entity.CodeResult, error)
}

func (r codeRepositoryStub) CodeOptions(ctx context.Context, userID int64) ([]entity.CodeOption, error) {
	return r.options(ctx, userID)
}

func (r codeRepositoryStub) RequestCode(ctx context.Context, userID, dayID, updateID int64) (entity.CodeResult, error) {
	return r.request(ctx, userID, dayID, updateID)
}

func TestCodeRequestsRejectInvalidIdentifiers(t *testing.T) {
	service := New(codeRepositoryStub{})
	for _, userID := range []int64{0, -1} {
		if _, err := service.CodeOptions(t.Context(), userID); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("CodeOptions(%d) error = %v", userID, err)
		}
	}
	for _, ids := range [][3]int64{{0, 1, 1}, {-1, 1, 1}, {1, 0, 1}, {1, -1, 1}, {1, 1, -1}} {
		if _, err := service.RequestCode(t.Context(), ids[0], ids[1], ids[2]); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("RequestCode(%v) error = %v", ids, err)
		}
	}
}

func TestCodeRequestsPreserveRepositoryErrors(t *testing.T) {
	want := errors.New("database unavailable")
	service := New(codeRepositoryStub{
		options: func(context.Context, int64) ([]entity.CodeOption, error) { return nil, want },
		request: func(_ context.Context, userID, dayID, updateID int64) (entity.CodeResult, error) {
			if userID != 7 || dayID != 8 || updateID != 0 {
				t.Fatal("request identifiers changed")
			}
			return entity.CodeResult{}, want
		},
	})
	if _, err := service.CodeOptions(t.Context(), 7); !errors.Is(err, want) {
		t.Fatalf("CodeOptions() error = %v", err)
	}
	if _, err := service.RequestCode(t.Context(), 7, 8, 0); !errors.Is(err, want) {
		t.Fatalf("RequestCode() error = %v", err)
	}
}
