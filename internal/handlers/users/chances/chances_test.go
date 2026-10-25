package chances_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	chancesentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/chances"
	usershandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users"
	testutil "github.com/letsssgooo/giveaways_bot/internal/handlers/users/internal/testutil"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
	promotionService "github.com/letsssgooo/giveaways_bot/internal/services/promotion"
)

func TestChancesShowsActivityScheduleAndPrize(t *testing.T) {
	handler, _, queue := testutil.NewHandler()
	handler.Promotion = promotionService.New(&testutil.PromotionRepository{ChancesData: chancesentity.Chances{
		Configured: true, RemainingDays: 7,
		ActiveDays: []chancesentity.ActivityDay{
			{Date: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), Partners: []string{"Кофе", "Книги"}},
			{Date: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC), Partners: []string{"Кофе"}},
		},
		Prizes: []chancesentity.FinalPrize{{Name: "Билеты на фестиваль", PlacesCount: 5, UnitsPerWinner: 2}},
	}})
	if err := handler.Handle(t.Context(), testutil.PrivateCallback(userview.ActionChances)); err != nil {
		t.Fatal(err)
	}
	reply := testutil.LastReply(t, queue)
	for _, want := range []string{
		"Твой фестивальный пропуск", "Накоплено шансов: 2 из 10", "Осталось дней участия: 7",
		"Билеты на фестиваль", "Победителей: 5; каждому: 2 шт.", "Активные дни: 2 (даты по МСК)",
		"25.10.2026 — Кофе, Книги", "24.10.2026 — Кофе", userview.ChancesRuleText,
	} {
		if !strings.Contains(reply.Text, want) {
			t.Fatalf("missing %q in %q", want, reply.Text)
		}
	}
	if buttons := reply.ReplyMarkup.InlineKeyboard; len(buttons) != 1 || buttons[0][0].CallbackData != userview.ActionHome {
		t.Fatalf("unexpected navigation: %+v", buttons)
	}
}

func TestChancesWithoutActivityOrSettings(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(strconv.FormatBool(configured), func(t *testing.T) {
			handler, _, queue := testutil.NewHandler()
			handler.Promotion = promotionService.New(&testutil.PromotionRepository{ChancesData: chancesentity.Chances{Configured: configured}})
			if err := handler.Handle(t.Context(), testutil.PrivateCallback(userview.ActionChances)); err != nil {
				t.Fatal(err)
			}
			text := testutil.LastReply(t, queue).Text
			for _, want := range []string{"Накоплено шансов: 0 из 10", userview.ChancesEmptyHistoryText, userview.ChancesMissingPrizeText} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %q", want, text)
				}
			}
			if strings.Contains(text, userview.ChancesMissingScheduleText) == configured || strings.Contains(text, "Осталось дней участия: 0") != configured {
				t.Fatalf("incorrect schedule state: %q", text)
			}
		})
	}
}

func TestChancesKeepsHistoryBeyondCapAndPaginates(t *testing.T) {
	handler, _, queue := testutil.NewHandler()
	repository := &testutil.PromotionRepository{ChancesData: chancesentity.Chances{Configured: true}}
	date := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	for i := range 12 {
		repository.ChancesData.ActiveDays = append(repository.ChancesData.ActiveDays, chancesentity.ActivityDay{Date: date.AddDate(0, 0, -i), Partners: []string{fmt.Sprintf("Партнер %d", i)}})
	}
	handler.Promotion = promotionService.New(repository)
	if err := handler.Handle(t.Context(), testutil.PrivateCallback(userview.ActionChances)); err != nil {
		t.Fatal(err)
	}
	first := testutil.LastReply(t, queue)
	for _, want := range []string{"Накоплено шансов: 10 из 10", "Активные дни: 12", userview.ChancesLimitText, "История: страница 1 из 2", "25.10.2026", "16.10.2026"} {
		if !strings.Contains(first.Text, want) {
			t.Fatalf("missing %q in %q", want, first.Text)
		}
	}
	if strings.Contains(first.Text, "15.10.2026") {
		t.Fatal("second page leaked into first")
	}
	next := first.ReplyMarkup.InlineKeyboard[0][0].CallbackData
	if next != "chances:1" || !usershandler.KnownAction(next) {
		t.Fatalf("invalid next button: %q", next)
	}
	// Старые кнопки и произвольный большой номер открывают последнюю доступную страницу.
	for _, action := range []string{next, "chances:" + strconv.Itoa(int(^uint(0)>>1))} {
		if err := handler.Handle(t.Context(), testutil.PrivateCallback(action)); err != nil {
			t.Fatal(err)
		}
		last := testutil.LastReply(t, queue)
		if !strings.Contains(last.Text, "История: страница 2 из 2") || !strings.Contains(last.Text, "14.10.2026 — Партнер 11") || strings.Contains(last.Text, "16.10.2026") {
			t.Fatalf("incorrect last page: %q", last.Text)
		}
		back := last.ReplyMarkup.InlineKeyboard[0][0].CallbackData
		if back != "chances:0" || !usershandler.KnownAction(back) {
			t.Fatalf("invalid back button: %q", back)
		}
	}
	if len(repository.ChancesData.ActiveDays) != 12 {
		t.Fatal("history changed after viewing")
	}
}

func TestInvalidChancesNavigation(t *testing.T) {
	for _, action := range []string{"chances:", "chances:-1", "chances:1:2", "chances:abc", "chances:999999999999999999999999"} {
		t.Run(action, func(t *testing.T) {
			handler, _, queue := testutil.NewHandler()
			if usershandler.KnownAction(action) {
				t.Fatal("invalid action recognized")
			}
			if err := handler.Handle(t.Context(), testutil.PrivateCallback(action)); err != nil {
				t.Fatal(err)
			}
			if testutil.LastReply(t, queue).Text != userview.StaleButtonText {
				t.Fatal("invalid action did not return to menu")
			}
		})
	}
}
