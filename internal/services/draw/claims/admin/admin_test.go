package admin

import (
	"context"
	"errors"
	"testing"

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
)

type awardAdminFake struct {
	access                    promotionadminentity.Access
	award                     awardsentity.Award
	locked, retried, replaced bool
}

func (f *awardAdminFake) Access(context.Context, int64) (promotionadminentity.Access, error) {
	return f.access, nil
}
func (f *awardAdminFake) LockAdmin(context.Context) error { f.locked = true; return nil }
func (f *awardAdminFake) ViewAdmin(context.Context, int64) (*awardsentity.Award, error) {
	return &f.award, nil
}
func (f *awardAdminFake) ListIssues(context.Context) ([]awardsentity.Award, error) {
	return []awardsentity.Award{{ID: 1, Kind: "daily", PartnerID: 10}, {ID: 2, Kind: "daily", PartnerID: 20}, {ID: 3, Kind: "final"}}, nil
}
func (f *awardAdminFake) Retry(context.Context, int64) error   { f.retried = true; return nil }
func (f *awardAdminFake) Replace(context.Context, int64) error { f.replaced = true; return nil }

func TestClaimAdministrationRestrictsPartnerScope(t *testing.T) {
	f := &awardAdminFake{access: promotionadminentity.Access{PartnerIDs: []int64{10}}, award: awardsentity.Award{ID: 1, Kind: "daily", PartnerID: 10}}
	service := NewClaimAdministration(f, f, f)
	issues, err := service.Issues(context.Background(), 501)
	if err != nil || len(issues) != 1 || issues[0].ID != 1 {
		t.Fatalf("issues=%v err=%v", issues, err)
	}
	if err := service.Retry(context.Background(), 501, 1); err != nil || !f.retried || !f.locked {
		t.Fatalf("own prize retry: err=%v locked=%v retried=%v", err, f.locked, f.retried)
	}
	f.award.Kind = "final"
	if err := service.Replace(context.Background(), 501, 1); !errors.Is(err, promotionadminentity.ErrForbidden) || f.replaced {
		t.Fatalf("partner replaced final prize: %v", err)
	}
	f.award.Kind, f.award.PartnerID = "daily", 20
	if err := service.Replace(context.Background(), 501, 1); !errors.Is(err, promotionadminentity.ErrForbidden) || f.replaced {
		t.Fatalf("partner replaced foreign prize: %v", err)
	}
}

func TestClaimAdministrationRechecksRevokedAccess(t *testing.T) {
	f := &awardAdminFake{access: promotionadminentity.Access{Organizer: true}, award: awardsentity.Award{ID: 1, Kind: "final"}}
	service := NewClaimAdministration(f, f, f)
	if _, err := service.View(context.Background(), 501, 1); err != nil {
		t.Fatal(err)
	}
	f.access = promotionadminentity.Access{}
	if err := service.Replace(context.Background(), 501, 1); !errors.Is(err, promotionadminentity.ErrForbidden) || f.replaced {
		t.Fatalf("revoked access allowed replacement: %v", err)
	}
}
