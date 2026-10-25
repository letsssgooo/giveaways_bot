package users

import (
	"context"
	"errors"
	"testing"
	"time"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/users"
)

type repositoryFunc func(context.Context, int64, int64) (entity.User, error)

func (f repositoryFunc) Ensure(ctx context.Context, telegramID, chatID int64) (entity.User, error) {
	return f(ctx, telegramID, chatID)
}

func TestEnsureReturnsExistingIdentity(t *testing.T) {
	want := entity.User{ID: 17, TelegramID: 101, ChatID: 101, StartedAt: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	ctx := t.Context()
	service := New(repositoryFunc(func(gotCtx context.Context, telegramID, chatID int64) (entity.User, error) {
		if gotCtx != ctx || telegramID != want.TelegramID || chatID != want.ChatID {
			t.Fatal("repository received incorrect context or identity")
		}
		return want, nil
	}))
	got, err := service.Ensure(ctx, want.TelegramID, want.ChatID)
	if err != nil || got != want {
		t.Fatalf("Ensure() = %+v, %v; want %+v", got, err, want)
	}
}

func TestEnsureRejectsInvalidIdentity(t *testing.T) {
	service := New(repositoryFunc(func(context.Context, int64, int64) (entity.User, error) {
		t.Fatal("repository must not be called for invalid identity")
		return entity.User{}, nil
	}))
	for _, identity := range [][2]int64{{0, 1}, {-1, 1}, {1, 0}, {1, -1}} {
		if _, err := service.Ensure(t.Context(), identity[0], identity[1]); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("Ensure(%v) error = %v", identity, err)
		}
	}
}

func TestEnsurePreservesRepositoryError(t *testing.T) {
	want := errors.New("repository unavailable")
	service := New(repositoryFunc(func(context.Context, int64, int64) (entity.User, error) {
		return entity.User{}, want
	}))
	if _, err := service.Ensure(t.Context(), 101, 101); !errors.Is(err, want) {
		t.Fatalf("Ensure() error = %v; want %v", err, want)
	}
}
