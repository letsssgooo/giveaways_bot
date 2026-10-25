package claims

import (
	"context"
	"errors"
	"strings"
	"testing"

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
)

func TestNormalizeClaimName(t *testing.T) {
	for _, name := range []string{"Иван", "  Анна-Мария  ", "О'Коннор", "李 明", strings.Repeat("я", 100)} {
		got, err := NormalizeName(name)
		if err != nil || got != strings.TrimSpace(name) {
			t.Fatalf("valid name rejected: %q, %v", name, err)
		}
	}
	for _, name := range []string{"", "   ", "\nИван", "Иван\n", "Иван\tПетров", "Иван\x00", strings.Repeat("я", 101), string([]byte{0xff})} {
		if _, err := NormalizeName(name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("invalid name accepted: %q, %v", name, err)
		}
	}
}

type claimRepositoryStub struct {
	calls       int
	first, last string
}

func (*claimRepositoryStub) View(context.Context, int64, int64) (*awardsentity.Award, error) {
	return nil, nil
}
func (*claimRepositoryStub) List(context.Context, int64) ([]awardsentity.Award, error) {
	return nil, nil
}
func (*claimRepositoryStub) AcceptConsent(context.Context, int64, int64) (*awardsentity.Award, error) {
	return nil, nil
}
func (r *claimRepositoryStub) Confirm(_ context.Context, _, _ int64, first, last string) (*awardsentity.Award, error) {
	r.calls++
	r.first, r.last = first, last
	return &awardsentity.Award{}, nil
}

func TestConfirmValidatesNamesBeforeRepository(t *testing.T) {
	repo := &claimRepositoryStub{}
	service := NewClaims(repo)
	if _, err := service.Confirm(t.Context(), 1, 2, "Иван", ""); !errors.Is(err, ErrInvalidName) || repo.calls != 0 {
		t.Fatalf("invalid name reached repository: %v", err)
	}
	if _, err := service.Confirm(t.Context(), 0, 2, "Иван", "Иванов"); !errors.Is(err, awardsentity.ErrAwardUnavailable) || repo.calls != 0 {
		t.Fatalf("invalid award reached repository: %v", err)
	}
	if _, err := service.Confirm(t.Context(), 1, 2, "  Иван ", " Иванов "); err != nil || repo.calls != 1 || repo.first != "Иван" || repo.last != "Иванов" {
		t.Fatalf("normalized names not stored: %v", err)
	}
}
