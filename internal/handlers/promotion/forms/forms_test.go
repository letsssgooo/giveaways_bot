package forms

import (
	"errors"
	"strings"
	"testing"
	"time"

	session "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/session"
	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
)

func TestDayFormUsesMoscowTimeAndPreservesNextMidnightWhenEditing(t *testing.T) {
	f := session.Flow{Target: 5, Parent: 7, Text: "2026-10-25\r\n09:15\r\n24:00\r\n2026-10-27 18:30\r\nДА\r\nВведите код при оплате.\r\nСохраните чек."}
	day, err := parseDay(f)
	if err != nil {
		t.Fatal(err)
	}
	if day.ID != 5 || day.PartnerID != 7 || !day.IsEnabled || day.Date.Format(time.DateOnly) != "2026-10-25" ||
		day.OpensAt.UTC().Format(time.RFC3339) != "2026-10-25T06:15:00Z" ||
		day.ClosesAt.UTC().Format(time.RFC3339) != "2026-10-25T21:00:00Z" ||
		day.PromoValidUntil == nil || day.PromoValidUntil.UTC().Format(time.RFC3339) != "2026-10-27T15:30:00Z" ||
		day.PromoInstructions != "Введите код при оплате.\nСохраните чек." {
		t.Fatalf("day form changed data or timezone: %+v", day)
	}
	f.Text = FormatDay(day)
	reopened, err := parseDay(f)
	if err != nil || !reopened.Date.Equal(day.Date) || !reopened.ClosesAt.Equal(day.ClosesAt) ||
		!reopened.OpensAt.Equal(day.OpensAt) || reopened.PromoValidUntil == nil ||
		!reopened.PromoValidUntil.Equal(*day.PromoValidUntil) || reopened.PromoInstructions != day.PromoInstructions {
		t.Fatalf("reopening saved day changed dates or instructions: %+v, %v", reopened, err)
	}
}

func TestDayFormRejectsInvalidDatesAndOnlyAllows2400AtClosing(t *testing.T) {
	for name, text := range map[string]string{
		"invalid date": "2026-02-30\n09:00\n24:00\n-\nда\n-",
		"opening 24":   "2026-10-25\n24:00\n24:00\n-\nда\n-",
		"closing 2430": "2026-10-25\n09:00\n24:30\n-\nда\n-",
		"bad expiry":   "2026-10-25\n09:00\n24:00\n2026-10-26 24:00\nда\n-",
		"bad flag":     "2026-10-25\n09:00\n24:00\n-\nможет быть\n-",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDay(session.Flow{Text: text}); !errors.Is(err, promotionadmin.ErrInvalid) {
				t.Fatalf("invalid form accepted: %v", err)
			}
		})
	}
	day, err := parseDay(session.Flow{Text: "2026-10-25\n00:00\n24:00\n-\nнет\n-"})
	if err != nil || day.IsEnabled || day.PromoValidUntil != nil || day.PromoInstructions != "" ||
		day.ClosesAt.Sub(day.OpensAt) != 24*time.Hour {
		t.Fatalf("full disabled day or optional fields parsed incorrectly: %+v, %v", day, err)
	}
}

func TestDrawFormPreservesPrizeQuantitiesAndSchedule(t *testing.T) {
	f := session.Flow{Target: 8, Text: "2026-11-01 00:00\nБилеты | 5 | 2 | -"}
	draw, err := parseDraw(f)
	if err != nil || draw.ID != 8 || draw.Kind != "final" || draw.PartnerDayID != 0 || len(draw.Prizes) != 1 ||
		draw.Prizes[0].PlacesCount != 5 || draw.Prizes[0].UnitsPerWinner != 2 || draw.Prizes[0].Instructions != "" ||
		draw.ScheduledAt.UTC().Format(time.RFC3339) != "2026-10-31T21:00:00Z" {
		t.Fatalf("final draw form: %+v, %v", draw, err)
	}
	f.Parent = 4
	f.Text = "2026-10-25 20:00\nГлавный приз | 1 | 3 | Получите у партнера\nПодарок | 10 | 1 | -"
	draw, err = parseDraw(f)
	if err != nil || draw.Kind != "daily" || draw.PartnerDayID != 4 || len(draw.Prizes) != 2 ||
		draw.Prizes[0].UnitsPerWinner != 3 || draw.Prizes[1].PlacesCount != 10 {
		t.Fatalf("daily draw prizes: %+v, %v", draw, err)
	}
	if FormatDraw(draw) != f.Text {
		t.Fatalf("saved draw cannot be reused as form input: %q", FormatDraw(draw))
	}
	for _, prize := range []string{"Билеты | 0 | 2 | -", "Билеты | 5 | -1 | -", "Билеты | пять | 2 | -", "Билеты | 5 | 2"} {
		if _, err := parseDraw(session.Flow{Text: "2026-11-01 00:00\n" + prize}); !errors.Is(err, promotionadmin.ErrInvalid) {
			t.Errorf("invalid prize accepted: %q, %v", prize, err)
		}
	}
}

func TestPoliciesFormRequiresExplicitPositivePeriodsAndKnownChoices(t *testing.T) {
	for _, choice := range []struct{ start, failure, storedStart, storedFailure string }{
		{"доставка", "вручную", "delivered", "manual"},
		{"УТВЕРЖДЕНИЕ", "РЕЗЕРВ", "approved", "reserve"},
	} {
		policy, err := parsePolicies("48\n" + choice.start + "\n" + choice.failure)
		if err != nil || policy.WinnerResponseHours != 48 ||
			policy.ResponseStartsOn != choice.storedStart || policy.UndeliverablePolicy != choice.storedFailure {
			t.Fatalf("policies form: %+v, %v", policy, err)
		}
	}
	for _, text := range []string{"0\nдоставка\nвручную", "48\nдоставка\nрезерв\n365", "48\nотправка\nвручную", "48\nдоставка\nзабыть"} {
		if _, err := parsePolicies(text); !errors.Is(err, promotionadmin.ErrInvalid) {
			t.Errorf("invalid policy accepted: %q, %v", text, err)
		}
	}
}

func TestConsentFormRequiresVersionAndTextTogether(t *testing.T) {
	valid := "2026-10-25\nСогласие на обработку данных.\n\nУсловия передачи партнеру."
	if err := Validate(session.Flow{Action: "consent", Text: valid}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"2026-10-25", "\nТекст согласия", "2026-10-25\n" + strings.Repeat(" \n", 2)} {
		if err := Validate(session.Flow{Action: "consent", Text: text}); !errors.Is(err, promotionadmin.ErrInvalid) {
			t.Errorf("incomplete consent accepted: %q, %v", text, err)
		}
	}
}
