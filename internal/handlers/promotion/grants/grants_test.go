package grants_test

import (
	"encoding/json"
	"testing"

	"github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/forms"
	"github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/grants"

	"github.com/go-telegram/bot/models"

	session "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/session"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
)

func TestGrantConfirmationKeepsPreviewRecipient(t *testing.T) {
	f := session.Flow{Action: "grant", Text: "@Some_User", Nonce: 101, RecipientID: 503}
	payload, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	dialog := &dialogs.Dialog{State: "admin:confirm", Payload: payload}
	update := &models.Update{ID: 102, CallbackQuery: &models.CallbackQuery{Data: "admin:confirm:101"}}
	username, id, ok := grants.GrantUsernameRequest(update, dialog)
	if !ok || username != "some_user" || id != 503 {
		t.Fatalf("confirmation request = %q, %d, %v", username, id, ok)
	}
	update.CallbackQuery.Data = "admin:confirm:99"
	if _, _, ok := grants.GrantUsernameRequest(update, dialog); ok {
		t.Fatal("stale confirmation must not request verification")
	}
	for _, verified := range []session.GrantVerification{
		{},
		{UpdateID: 101, Username: "some_user", TelegramID: 503},
		{UpdateID: 102, Username: "another_user", TelegramID: 503},
		{UpdateID: 102, Username: "some_user", TelegramID: 504},
		{UpdateID: 102, Username: "some_user", TelegramID: 503, Problem: "nickname changed"},
	} {
		if _, err := grants.Recipient(session.Request{UpdateID: 102}, f, verified); err == nil {
			t.Fatalf("accepted mismatched or failed verification: %+v", verified)
		}
	}
	verified := session.GrantVerification{UpdateID: 102, Username: "some_user", TelegramID: 503}
	if id, err := grants.Recipient(session.Request{UpdateID: 102}, f, verified); err != nil || id != 503 {
		t.Fatalf("verified recipient = %d, %v", id, err)
	}
}

func TestGrantInputAcceptsUsernameAndNumericID(t *testing.T) {
	for _, value := range []string{"@Some_User", "503"} {
		if err := forms.Validate(session.Flow{Action: "grant", Text: value}); err != nil {
			t.Fatalf("valid input %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"@", "@имя", "@some user", "@some/user", "-503", "0"} {
		if err := forms.Validate(session.Flow{Action: "grant", Text: value}); err == nil {
			t.Fatalf("invalid input %q accepted", value)
		}
	}
	if id, err := grants.Recipient(session.Request{}, session.Flow{Action: "grant", Text: "503"}, session.GrantVerification{}); err != nil || id != 503 {
		t.Fatalf("numeric grant requires username verification: %d, %v", id, err)
	}
}
