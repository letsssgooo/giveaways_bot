package codes_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	entity "github.com/letsssgooo/giveaways_bot/internal/entity/participation"
	codesflow "github.com/letsssgooo/giveaways_bot/internal/handlers/users/codes"
	testutil "github.com/letsssgooo/giveaways_bot/internal/handlers/users/internal/testutil"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
	service "github.com/letsssgooo/giveaways_bot/internal/services/participation"
)

func TestCodeMenuSelectsAndRetrievesOwnHistory(t *testing.T) {
	day := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for _, count := range []int{0, 1, 2, codesflow.PageSize + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			handler, _, queue := testutil.NewHandler()
			repository := &testutil.CodeRepository{Result: entity.CodeResult{Outcome: entity.CodeRepeated, PartnerDayID: 1, PartnerName: "Партнер", Code: "OLD-CODE", Date: day, Chances: 1}}
			for i := range count {
				repository.Options = append(repository.Options, entity.CodeOption{PartnerDayID: int64(i + 1), PartnerName: "Партнер", Date: day, HasCode: true})
			}
			handler.Participation = service.New(repository)
			if err := handler.Handle(t.Context(), testutil.PrivateCallback(userview.ActionCode)); err != nil {
				t.Fatal(err)
			}
			if repository.UserID != 3 {
				t.Fatalf("wrong user: %d", repository.UserID)
			}
			reply := testutil.LastReply(t, queue)
			switch count {
			case 0:
				if reply.Text != userview.NoCodesText || repository.Requests != 0 {
					t.Fatalf("unexpected empty menu: %+v", reply)
				}
			case 1:
				if !strings.Contains(reply.Text, "OLD-CODE") || repository.Requests != 1 || repository.DayID != 1 || repository.UpdateID != 43 {
					t.Fatalf("single option wasn't requested: %+v", reply)
				}
			default:
				if repository.Requests != 0 {
					t.Fatal("selection menu issued a code")
				}
				buttons := reply.ReplyMarkup.InlineKeyboard
				if !strings.Contains(buttons[0][0].Text, "24.10.2026") || !strings.Contains(buttons[0][0].Text, userview.ReceivedCodeSuffix) || buttons[0][0].CallbackData != "partner:code:1" {
					t.Fatalf("history button: %+v", buttons[0])
				}
				if count > codesflow.PageSize {
					if len(buttons) != codesflow.PageSize+2 || buttons[codesflow.PageSize][0].CallbackData != "codes:1" {
						t.Fatalf("missing pagination: %+v", buttons)
					}
					if err := handler.Handle(t.Context(), testutil.PrivateCallback("codes:1")); err != nil {
						t.Fatal(err)
					}
					if testutil.LastReply(t, queue).ReplyMarkup.InlineKeyboard[0][0].CallbackData != "partner:code:11" {
						t.Fatal("wrong next page")
					}
				}
			}
		})
	}
}

func TestCodeOutcomesPreserveJoinAction(t *testing.T) {
	validUntil := time.Date(2026, 10, 27, 12, 30, 0, 0, time.UTC)
	for _, outcome := range []entity.CodeOutcome{entity.CodeIssued, entity.CodeRepeated, entity.CodeExhausted, entity.CodeClosed, entity.CodeUnavailable} {
		t.Run(string(outcome), func(t *testing.T) {
			handler, _, queue := testutil.NewHandler()
			repository := &testutil.CodeRepository{Result: entity.CodeResult{Outcome: outcome, PartnerDayID: 7, PartnerName: "Партнер", Code: "PROMO-007", Instructions: "Примените на сайте партнера.", Date: validUntil, ValidUntil: &validUntil, Chances: 2}}
			handler.Participation = service.New(repository)
			if err := handler.Handle(t.Context(), testutil.PrivateCallback("partner:code:7")); err != nil {
				t.Fatal(err)
			}
			if repository.Requests != 1 || repository.UserID != 3 || repository.DayID != 7 || repository.UpdateID != 43 {
				t.Fatal("incorrect request identity")
			}
			reply := testutil.LastReply(t, queue)
			switch outcome {
			case entity.CodeIssued, entity.CodeRepeated:
				for _, want := range []string{"PROMO-007", "Партнер", "27.10.2026 15:30 (МСК)", "Примените на сайте партнера.", "2 из 10"} {
					if !strings.Contains(reply.Text, want) {
						t.Fatalf("reply misses %q: %s", want, reply.Text)
					}
				}
			case entity.CodeExhausted:
				if !strings.Contains(reply.Text, "уже закончились") || strings.Contains(reply.Text, "PROMO-007") {
					t.Fatal(reply.Text)
				}
			case entity.CodeClosed, entity.CodeUnavailable:
				if reply.Text != userview.ClosedPartnerText {
					t.Fatal(reply.Text)
				}
			}
			buttons := reply.ReplyMarkup.InlineKeyboard
			if outcome == entity.CodeIssued || outcome == entity.CodeExhausted {
				if buttons[0][0].Text != userview.JoinButtonText || buttons[0][0].CallbackData != "partner:join:7" {
					t.Fatal("missing participation path")
				}
			} else if len(buttons) != 1 || buttons[0][0].CallbackData != userview.ActionHome {
				t.Fatal("unexpected action after closed/historical result")
			}
		})
	}
}

func TestCodeErrorsPropagateForTransactionRollback(t *testing.T) {
	want := errors.New("database unavailable")
	for _, action := range []string{userview.ActionCode, "partner:code:7"} {
		handler, _, _ := testutil.NewHandler()
		handler.Participation = service.New(&testutil.CodeRepository{Err: want})
		if err := handler.Handle(t.Context(), testutil.PrivateCallback(action)); !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestCodePageNavigationDoesNotIssueWhenOptionsChanged(t *testing.T) {
	handler, _, queue := testutil.NewHandler()
	repository := &testutil.CodeRepository{Options: []entity.CodeOption{{PartnerDayID: 7, PartnerName: "Новый партнер"}}}
	handler.Participation = service.New(repository)
	if err := handler.Handle(t.Context(), testutil.PrivateCallback("codes:2")); err != nil {
		t.Fatal(err)
	}
	if repository.Requests != 0 || testutil.LastReply(t, queue).ReplyMarkup.InlineKeyboard[0][0].CallbackData != "partner:code:7" {
		t.Fatal("navigation must display the remaining option, not issue a code")
	}
}
