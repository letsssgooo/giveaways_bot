package users_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	testutil "github.com/letsssgooo/giveaways_bot/internal/handlers/users/internal/testutil"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
)

func TestMessageCommandsAndInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		text     string
		want     string
		keyboard bool
	}{
		{"start", "/start", userview.WelcomeText, true},
		{"start with payload", "/start campaign", userview.WelcomeText, true},
		{"addressed to bot", "/start@gIvEaWaYsBoT", userview.WelcomeText, true},
		{"menu", "/menu", userview.WelcomeText, true},
		{"help", "/help", userview.HelpText, true},
		{"uppercase help", "/HELP@GiveawaysBot", userview.HelpText, true},
		{"unknown command", "/missing", userview.InvalidInputText, true},
		{"plain text", "<b>hello</b>", userview.InvalidInputText, true},
		{"media or empty message", "", userview.InvalidInputText, true},
		{"whitespace", "   ", userview.InvalidInputText, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, repository, queue := testutil.NewHandler()
			if err := handler.Handle(t.Context(), testutil.PrivateMessage(tc.text)); err != nil {
				t.Fatal(err)
			}
			wantMessages := 1
			if strings.HasPrefix(tc.text, "/start") {
				wantMessages = 2
			}
			if repository.Calls != 1 || len(queue.Messages) != wantMessages {
				t.Fatalf("user calls=%d, outbox messages=%d", repository.Calls, len(queue.Messages))
			}
			got := queue.Messages[len(queue.Messages)-1]
			if got.UserID != 3 || got.UpdateID != 42 || got.Kind != "send_message" || got.Key != "telegram:update:42:reply" {
				t.Fatalf("incorrect outbox metadata: %+v", got)
			}
			var payload testutil.MessageReply
			if err := json.Unmarshal(got.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.ChatID != 101 || payload.Text != tc.want || (payload.ReplyMarkup != nil) != tc.keyboard {
				t.Fatalf("incorrect reply: %+v", payload)
			}
			if tc.keyboard {
				buttons := payload.ReplyMarkup.InlineKeyboard
				wantButtons := 1
				if tc.want == userview.WelcomeText {
					wantButtons = 5
				}
				if len(buttons) != wantButtons {
					t.Fatalf("incorrect keyboard: %+v", buttons)
				}
			}
		})
	}
}

func TestIgnoredMessagesDoNotCreateUsersOrReplies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update func() *models.Update
	}{
		{"nil update", func() *models.Update { return nil }},
		{"empty update", func() *models.Update { return &models.Update{} }},
		{"other bot command", func() *models.Update { return testutil.PrivateMessage("/start@AnotherBot") }},
		{"invalid update ID", func() *models.Update { u := testutil.PrivateMessage("/start"); u.ID = -1; return u }},
		{"missing sender", func() *models.Update { u := testutil.PrivateMessage("/start"); u.Message.From = nil; return u }},
		{"invalid sender ID", func() *models.Update { u := testutil.PrivateMessage("/start"); u.Message.From.ID = 0; return u }},
		{"bot sender", func() *models.Update { u := testutil.PrivateMessage("/start"); u.Message.From.IsBot = true; return u }},
		{"group message", func() *models.Update {
			u := testutil.PrivateMessage("/start")
			u.Message.Chat.Type = models.ChatTypeGroup
			return u
		}},
		{"channel message", func() *models.Update {
			u := testutil.PrivateMessage("/start")
			u.Message.Chat.Type = models.ChatTypeChannel
			return u
		}},
		{"chat sender mismatch", func() *models.Update { u := testutil.PrivateMessage("/start"); u.Message.Chat.ID = 202; return u }},
		{"edited message", func() *models.Update {
			u := testutil.PrivateMessage("/start")
			u.EditedMessage = u.Message
			u.Message = nil
			return u
		}},
		{"inline callback", func() *models.Update {
			u := testutil.PrivateCallback("help")
			u.CallbackQuery.Message.Message = nil
			u.CallbackQuery.InlineMessageID = "inline"
			return u
		}},
		{"group callback", func() *models.Update {
			u := testutil.PrivateCallback("help")
			u.CallbackQuery.Message.Message.Chat.Type = models.ChatTypeGroup
			return u
		}},
		{"bot callback", func() *models.Update {
			u := testutil.PrivateCallback("help")
			u.CallbackQuery.From.IsBot = true
			return u
		}},
		{"empty callback ID", func() *models.Update { u := testutil.PrivateCallback("help"); u.CallbackQuery.ID = ""; return u }},
		{"invalid callback message type", func() *models.Update {
			u := testutil.PrivateCallback("help")
			u.CallbackQuery.Message.Type = 5
			return u
		}},
		{"missing inaccessible message", func() *models.Update {
			u := testutil.PrivateCallback("help")
			u.CallbackQuery.Message.Type = models.MaybeInaccessibleMessageTypeInaccessibleMessage
			return u
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, repository, queue := testutil.NewHandler()
			if err := handler.Handle(t.Context(), tc.update()); err != nil {
				t.Fatal(err)
			}
			if repository.Calls != 0 || len(queue.Messages) != 0 {
				t.Fatalf("ignored update persisted user or reply: users=%d, messages=%d", repository.Calls, len(queue.Messages))
			}
		})
	}
}

func TestCallbackAcknowledgmentPrecedesResponse(t *testing.T) {
	for _, data := range []string{"help", "removed_action", "", "<user supplied>"} {
		t.Run(data, func(t *testing.T) {
			handler, repository, queue := testutil.NewHandler()
			if err := handler.Handle(t.Context(), testutil.PrivateCallback(data)); err != nil {
				t.Fatal(err)
			}
			if repository.Calls != 1 || len(queue.Messages) != 2 {
				t.Fatalf("users=%d, messages=%d", repository.Calls, len(queue.Messages))
			}
			ack, reply := queue.Messages[0], queue.Messages[1]
			if ack.Kind != "answer_callback_query" || ack.Key != "telegram:update:43:callback" || reply.Kind != "send_message" || reply.Key != "telegram:update:43:reply" {
				t.Fatalf("incorrect callback response order: %+v", queue.Messages)
			}
			var payload testutil.CallbackAnswer
			if err := json.Unmarshal(ack.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.CallbackQueryID != "query-id" || (payload.Text == "") != (data == "help") {
				t.Fatalf("incorrect callback acknowledgment: %+v", payload)
			}
			var response testutil.MessageReply
			if err := json.Unmarshal(reply.Payload, &response); err != nil {
				t.Fatal(err)
			}
			want := userview.StaleButtonText
			if data == "help" {
				want = userview.HelpText
			}
			if response.ChatID != 101 || response.Text != want {
				t.Fatalf("incorrect callback response: %+v", response)
			}
		})
	}
}

func TestInaccessibleCallbackOnlyAcknowledged(t *testing.T) {
	handler, repository, queue := testutil.NewHandler()
	update := testutil.PrivateCallback("help")
	update.CallbackQuery.Message = models.MaybeInaccessibleMessage{
		Type:                models.MaybeInaccessibleMessageTypeInaccessibleMessage,
		InaccessibleMessage: &models.InaccessibleMessage{Chat: models.Chat{ID: 101, Type: models.ChatTypePrivate}},
	}
	if err := handler.Handle(t.Context(), update); err != nil {
		t.Fatal(err)
	}
	if repository.Calls != 1 || len(queue.Messages) != 1 || queue.Messages[0].Kind != "answer_callback_query" {
		t.Fatalf("unexpected inaccessible callback response: users=%d messages=%+v", repository.Calls, queue.Messages)
	}
	var payload testutil.CallbackAnswer
	if err := json.Unmarshal(queue.Messages[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Text != userview.StaleButtonText {
		t.Fatalf("acknowledgment = %q", payload.Text)
	}
}

func TestPersistenceErrorsPropagate(t *testing.T) {
	want := errors.New("database unavailable")
	for _, callback := range []bool{false, true} {
		for _, failingRepository := range []bool{false, true} {
			handler, repository, queue := testutil.NewHandler()
			if failingRepository {
				repository.Err = want
			} else {
				queue.Err = want
			}
			update := testutil.PrivateMessage("/start")
			if callback {
				update = testutil.PrivateCallback("help")
			}
			if err := handler.Handle(t.Context(), update); !errors.Is(err, want) {
				t.Fatalf("callback=%v, repository=%v: error=%v", callback, failingRepository, err)
			}
			if len(queue.Messages) != 0 {
				t.Fatal("unexpected reply after persistence error")
			}
		}
	}
}

func TestRepeatedUpdateUsesIdenticalDeduplicationKeys(t *testing.T) {
	handler, _, queue := testutil.NewHandler()
	for range 2 {
		if err := handler.Handle(t.Context(), testutil.PrivateCallback("help")); err != nil {
			t.Fatal(err)
		}
	}
	if len(queue.Messages) != 4 || queue.Messages[0].Key != queue.Messages[2].Key || queue.Messages[1].Key != queue.Messages[3].Key {
		t.Fatalf("unstable deduplication keys: %+v", queue.Messages)
	}
	for _, message := range queue.Messages {
		if !strings.HasPrefix(message.Key, "telegram:update:43:") {
			t.Fatalf("key does not identify the update: %q", message.Key)
		}
	}
}
