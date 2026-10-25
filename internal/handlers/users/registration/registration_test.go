package registration_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	registrationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation/registration"
	testutil "github.com/letsssgooo/giveaways_bot/internal/handlers/users/internal/testutil"
	registrationflow "github.com/letsssgooo/giveaways_bot/internal/handlers/users/registration"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
)

func TestRegistrationCheckRejectsUntrustedCallbacks(t *testing.T) {
	valid := func() *models.Update { return testutil.PrivateCallback("registration:check:7") }
	for _, tc := range []struct {
		name   string
		update func() *models.Update
	}{
		{"nil", func() *models.Update { return nil }},
		{"message", func() *models.Update { return testutil.PrivateMessage("registration:check:7") }},
		{"group", func() *models.Update {
			u := valid()
			u.CallbackQuery.Message.Message.Chat.Type = models.ChatTypeGroup
			return u
		}},
		{"other user", func() *models.Update { u := valid(); u.CallbackQuery.From.ID = 202; return u }},
		{"bot", func() *models.Update { u := valid(); u.CallbackQuery.From.IsBot = true; return u }},
		{"inaccessible", func() *models.Update {
			u := valid()
			u.CallbackQuery.Message.Type = models.MaybeInaccessibleMessageTypeInaccessibleMessage
			return u
		}},
		{"missing callback ID", func() *models.Update { u := valid(); u.CallbackQuery.ID = ""; return u }},
		{"invalid day", func() *models.Update { return testutil.PrivateCallback("registration:check:-1") }},
		{"invalid suffix", func() *models.Update { return testutil.PrivateCallback("registration:check:7:101") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := registrationflow.RegistrationCheck(tc.update()); ok {
				t.Fatal("unsafe callback accepted")
			}
		})
	}
	if userID, dayID, ok := registrationflow.RegistrationCheck(valid()); !ok || userID != 101 || dayID != 7 {
		t.Fatalf("valid check: %d, %d, %v", userID, dayID, ok)
	}
}

func TestRegistrationResponsesAndRetryButtons(t *testing.T) {
	day := &registrationentity.RegistrationDay{ID: 7, Name: "Партнер", Open: true, SubscriptionURL: "https://t.me/partner"}
	for _, tc := range []struct {
		status registrationentity.RegistrationStatus
		want   string
		retry  bool
	}{
		{registrationentity.Registered, "зарегистрирована", false},
		{registrationentity.AlreadyRegistered, "уже участвуешь", false},
		{registrationentity.NotSubscribed, "Пока не вижу подписку", true},
		{registrationentity.VerificationFailed, registrationflow.SubscriptionFailure, true},
		{registrationentity.Closed, userview.ClosedPartnerText, false},
		{registrationentity.Unavailable, userview.ClosedPartnerText, false},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			handler, _, queue := testutil.NewHandler()
			service := &testutil.RegistrationStub{Result: registrationentity.RegistrationResult{Status: tc.status, Day: day, Chances: 1}}
			handler.WithRegistration(service, registrationentity.Verification{})
			if err := handler.Handle(t.Context(), testutil.PrivateCallback("registration:check:7")); err != nil {
				t.Fatal(err)
			}
			reply := testutil.LastReply(t, queue)
			if service.Calls != 1 || !strings.Contains(reply.Text, tc.want) {
				t.Fatalf("reply=%+v calls=%d", reply, service.Calls)
			}
			foundRetry := false
			for _, row := range reply.ReplyMarkup.InlineKeyboard {
				for _, button := range row {
					if button.CallbackData == "registration:check:7" {
						foundRetry = true
					}
				}
			}
			if foundRetry != tc.retry {
				t.Fatalf("retry=%v, want %v", foundRetry, tc.retry)
			}
			if len(queue.Messages) != 2 || queue.Messages[0].Kind != "answer_callback_query" {
				t.Fatal("missing callback acknowledgment")
			}
		})
	}
}

func TestJoinExistingRegistrationIgnoresClosedAdmission(t *testing.T) {
	handler, _, queue := testutil.NewHandler()
	service := &testutil.RegistrationStub{Result: registrationentity.RegistrationResult{Day: &registrationentity.RegistrationDay{ID: 7, Registered: true, Open: false}}}
	handler.WithRegistration(service, registrationentity.Verification{})
	if err := handler.Handle(t.Context(), testutil.PrivateCallback("partner:join:7")); err != nil {
		t.Fatal(err)
	}
	if testutil.LastReply(t, queue).Text != registrationflow.AlreadyRegistered || service.Calls != 0 {
		t.Fatal("existing registration was checked again")
	}
}

func TestRegistrationDatabaseErrorPropagates(t *testing.T) {
	want := errors.New("database unavailable")
	for _, action := range []string{"partner:join:7", "registration:check:7"} {
		handler, _, _ := testutil.NewHandler()
		handler.WithRegistration(&testutil.RegistrationStub{Err: want}, registrationentity.Verification{})
		if err := handler.Handle(t.Context(), testutil.PrivateCallback(action)); !errors.Is(err, want) {
			t.Fatalf("%s: %v", action, err)
		}
	}
}

func TestJoinPromptDoesNotRegisterUser(t *testing.T) {
	handler, _, queue := testutil.NewHandler()
	day := &registrationentity.RegistrationDay{ID: 7, Name: "Партнер", Open: true, SubscriptionURL: "https://t.me/partner"}
	service := &testutil.RegistrationStub{Result: registrationentity.RegistrationResult{Day: day}}
	handler.WithRegistration(service, registrationentity.Verification{})
	if err := handler.Handle(t.Context(), testutil.PrivateCallback("partner:join:7")); err != nil {
		t.Fatal(err)
	}
	reply := testutil.LastReply(t, queue)
	buttons := reply.ReplyMarkup.InlineKeyboard
	if reply.Text != fmt.Sprintf(registrationflow.SubscriptionPrompt, day.Name) || service.Calls != 0 || len(buttons) != 3 || buttons[0][0].URL != day.SubscriptionURL || buttons[1][0].CallbackData != "registration:check:7" {
		t.Fatalf("incorrect prompt: %+v", reply)
	}
}
