package claims

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
	userentity "github.com/letsssgooo/giveaways_bot/internal/entity/users"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type claimsStub struct {
	award                     awardsentity.Award
	consents, confirms, views int
	err                       error
}

func (s *claimsStub) View(_ context.Context, awardID, userID int64) (*awardsentity.Award, error) {
	s.views++
	if s.err != nil {
		return nil, s.err
	}
	if s.award.ID != awardID || s.award.UserID != userID {
		return nil, nil
	}
	copy := s.award
	return &copy, nil
}
func (s *claimsStub) List(context.Context, int64) ([]awardsentity.Award, error) {
	return []awardsentity.Award{s.award}, s.err
}
func (s *claimsStub) AcceptConsent(ctx context.Context, awardID, userID int64) (*awardsentity.Award, error) {
	s.consents++
	s.award.ConsentGivenAt = &s.award.Now
	return s.View(ctx, awardID, userID)
}
func (s *claimsStub) Confirm(ctx context.Context, awardID, userID int64, first, last string) (*awardsentity.Award, error) {
	s.confirms++
	if s.award.ConsentGivenAt == nil {
		return nil, awardsentity.ErrConsentRequired
	}
	s.award.FirstName, s.award.LastName, s.award.ConfirmedAt, s.award.State = first, last, &s.award.Now, "confirmed"
	return s.View(ctx, awardID, userID)
}

type claimUserStub struct{ calls int }

func (s *claimUserStub) Ensure(_ context.Context, telegramID, chatID int64) (userentity.User, error) {
	s.calls++
	return userentity.User{ID: 7, TelegramID: telegramID, ChatID: chatID}, nil
}

type claimDialogsStub struct{ dialog *dialogs.Dialog }

func (d *claimDialogsStub) Load(context.Context, int64) (*dialogs.Dialog, error) {
	return d.dialog, nil
}
func (d *claimDialogsStub) Save(_ context.Context, _ int64, state string, payload json.RawMessage, ttl time.Duration) error {
	if ttl != 30*time.Minute {
		return errors.New("unexpected TTL")
	}
	d.dialog = &dialogs.Dialog{State: state, Payload: append(json.RawMessage(nil), payload...)}
	return nil
}
func (d *claimDialogsStub) Clear(context.Context, int64) error { d.dialog = nil; return nil }

type claimReply struct {
	ChatID int64                        `json:"chat_id"`
	Text   string                       `json:"text"`
	Markup *models.InlineKeyboardMarkup `json:"reply_markup"`
}
type claimQueue struct {
	replies         []claimReply
	acknowledgments int
	keys            []string
}

func (o *claimQueue) Enqueue(_ context.Context, _, _ int64, kind string, payload json.RawMessage, key string) error {
	o.keys = append(o.keys, key)
	if kind == protocol.MessageKindCallbackAnswer {
		o.acknowledgments++
		return nil
	}
	var reply claimReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return err
	}
	o.replies = append(o.replies, reply)
	return nil
}
func newClaimsHandler() (*ClaimsHandler, *claimsStub, *claimDialogsStub, *claimQueue) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Hour)
	service := &claimsStub{award: awardsentity.Award{ID: 9, UserID: 7, TelegramID: 101, Current: true, State: "notified", Now: now, Deadline: &deadline, PrizeName: "Билет", PartnerName: "Партнер", Units: 2, Instructions: "Сохрани билет", ConsentVersion: "v1", ConsentText: "Полный текст согласия"}}
	dialog, queue := &claimDialogsStub{}, &claimQueue{}
	return NewClaims(service, userservice.New(&claimUserStub{}), dialog, queue, "GiveawaysBot"), service, dialog, queue
}
func claimMessage(id int64, text string) *models.Update {
	return &models.Update{ID: id, Message: &models.Message{From: &models.User{ID: 101, FirstName: "НЕ ИСПОЛЬЗОВАТЬ"}, Chat: models.Chat{ID: 101, Type: models.ChatTypePrivate}, Text: text}}
}
func claimCallback(id int64, data string) *models.Update {
	return &models.Update{ID: id, CallbackQuery: &models.CallbackQuery{ID: "query", From: models.User{ID: 101}, Data: data, Message: models.MaybeInaccessibleMessage{Message: claimMessage(id, "").Message}}}
}
func handleClaim(t *testing.T, h *ClaimsHandler, update *models.Update) {
	t.Helper()
	if handled, err := h.Handle(t.Context(), update); !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}

func TestClaimExplicitConsentAndConfirmedReplay(t *testing.T) {
	h, service, dialog, queue := newClaimsHandler()
	handleClaim(t, h, claimCallback(1, "prize:claim:9"))
	if dialog.dialog.State != "prize:consent" || service.consents != 0 {
		t.Fatal("claim click must not accept consent")
	}
	if !strings.Contains(queue.replies[0].Text, service.award.ConsentText) || queue.replies[0].Markup == nil {
		t.Fatal("full consent and explicit buttons missing")
	}
	handleClaim(t, h, claimMessage(2, "Иван"))
	if dialog.dialog.State != "prize:consent" || service.consents != 0 {
		t.Fatal("name before consent must not advance")
	}
	handleClaim(t, h, claimCallback(3, "prize:consent:9:1"))
	if service.consents != 1 || dialog.dialog.State != "prize:first_name" {
		t.Fatal("explicit consent did not advance")
	}
	// New handler instance uses the persisted dialogue after a restart.
	h = NewClaims(service, h.users, dialog, queue, "GiveawaysBot")
	handleClaim(t, h, claimMessage(4, "  Анна-Мария "))
	handleClaim(t, h, claimMessage(5, "О'Коннор"))
	if dialog.dialog.State != "prize:confirm" || service.confirms != 0 {
		t.Fatal("names must wait for explicit confirmation")
	}
	handleClaim(t, h, claimCallback(6, "prize:confirm:9:5"))
	if service.confirms != 1 || service.award.FirstName != "Анна-Мария" || service.award.LastName != "О'Коннор" || dialog.dialog != nil {
		t.Fatal("confirmation did not persist names and close dialogue")
	}
	service.award.Now = service.award.Deadline.Add(time.Hour)
	handleClaim(t, h, claimCallback(7, "prize:claim:9"))
	handleClaim(t, h, claimCallback(8, "prize:confirm:9:5"))
	if service.confirms != 1 || service.consents != 1 || !strings.Contains(queue.replies[len(queue.replies)-1].Text, "подтверждено") {
		t.Fatal("confirmed replay must be read only, even after deadline")
	}
	if queue.acknowledgments != 5 {
		t.Fatalf("missing callback acknowledgment: %d", queue.acknowledgments)
	}
}

func TestClaimForeignAndGroupAccess(t *testing.T) {
	for _, change := range []func(*models.Update){
		func(u *models.Update) { u.CallbackQuery.Message.Message.Chat.Type = models.ChatTypeGroup },
		func(u *models.Update) { u.CallbackQuery.Message.Message.Chat.ID = 202 },
		func(u *models.Update) { u.CallbackQuery.From.IsBot = true },
		func(u *models.Update) { u.CallbackQuery.Message.Message = nil },
	} {
		h, service, _, queue := newClaimsHandler()
		update := claimCallback(1, "prize:claim:9")
		change(update)
		handleClaim(t, h, update)
		if service.views != 0 || len(queue.replies) != 0 || queue.acknowledgments != 0 {
			t.Fatal("unsafe chat reached claim service")
		}
	}
	h, service, dialog, queue := newClaimsHandler()
	service.award.UserID = 8
	handleClaim(t, h, claimCallback(1, "prize:claim:9"))
	if service.consents != 0 || service.confirms != 0 || dialog.dialog != nil || !strings.Contains(queue.replies[0].Text, "недоступен") {
		t.Fatal("another user's award is accessible")
	}
}

func TestClaimStaleConsentAndConfirmation(t *testing.T) {
	h, service, dialog, queue := newClaimsHandler()
	handleClaim(t, h, claimCallback(1, "prize:claim:9"))
	handleClaim(t, h, claimCallback(2, "prize:claim:9"))
	handleClaim(t, h, claimCallback(3, "prize:consent:9:1"))
	if service.consents != 0 || dialog.dialog.State != "prize:consent" {
		t.Fatal("old consent click accepted")
	}
	handleClaim(t, h, claimCallback(4, "prize:consent:9:2"))
	handleClaim(t, h, claimMessage(5, "Иван"))
	handleClaim(t, h, claimMessage(6, "Иванов"))
	handleClaim(t, h, claimCallback(7, "prize:confirm:9:5"))
	if service.confirms != 0 || dialog.dialog.State != "prize:confirm" {
		t.Fatal("stale confirmation accepted")
	}
	dialog.dialog = nil // Expired dialogue cannot recover names from a stale button.
	handleClaim(t, h, claimCallback(8, "prize:confirm:9:6"))
	if service.confirms != 0 || !strings.Contains(queue.replies[len(queue.replies)-1].Text, "устарела") {
		t.Fatal("expired dialogue accepted")
	}
}

func TestClaimExpiryAndReplacementDuringInput(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		h, service, dialog, queue := newClaimsHandler()
		handleClaim(t, h, claimCallback(1, "prize:claim:9"))
		handleClaim(t, h, claimCallback(2, "prize:consent:9:1"))
		if replacement {
			service.award.Current = false
		} else {
			service.award.Now = *service.award.Deadline
		}
		handleClaim(t, h, claimMessage(3, "Иван"))
		if dialog.dialog != nil || service.confirms != 0 {
			t.Fatal("unavailable award collected a name")
		}
		if replacement && !strings.Contains(queue.replies[len(queue.replies)-1].Text, "недоступен") {
			t.Fatal("wrong replacement response")
		}
		if !replacement && !strings.Contains(queue.replies[len(queue.replies)-1].Text, "истек") {
			t.Fatal("deadline boundary was accepted")
		}
	}
}

func TestClaimDeclineAndCommands(t *testing.T) {
	h, service, dialog, _ := newClaimsHandler()
	handleClaim(t, h, claimCallback(1, "prize:claim:9"))
	handleClaim(t, h, claimCallback(2, "prize:decline:9:1"))
	if service.consents != 0 || dialog.dialog != nil {
		t.Fatal("decline stored consent or retained dialogue")
	}
	for _, command := range []string{"/menu", "/start", "/admin", "/cancel"} {
		handleClaim(t, h, claimCallback(3, "prize:claim:9"))
		handled, err := h.Handle(t.Context(), claimMessage(4, command))
		if err != nil || dialog.dialog != nil || handled != (command == "/cancel") {
			t.Fatalf("command %s: handled=%v err=%v", command, handled, err)
		}
	}
	if handled, err := h.Handle(t.Context(), claimCallback(5, "prizeadmin:home")); handled || err != nil {
		t.Fatal("admin callback intercepted")
	}
}

func TestClaimLongConsentSplitsUTF16AndNamesRejectControls(t *testing.T) {
	h, service, dialog, queue := newClaimsHandler()
	service.award.ConsentText = strings.Repeat("🎉", 5000)
	handleClaim(t, h, claimCallback(1, "prize:claim:9"))
	if len(queue.replies) < 3 {
		t.Fatal("long consent was not split")
	}
	var texts []string
	for i, reply := range queue.replies {
		if len(utf16.Encode([]rune(reply.Text))) > protocol.MaxMessageLength {
			t.Fatal("Telegram UTF16 limit exceeded")
		}
		if (reply.Markup != nil) != (i == len(queue.replies)-1) {
			t.Fatal("consent buttons should follow the entire text")
		}
		texts = append(texts, reply.Text)
	}
	if !strings.Contains(strings.Join(texts, ""), service.award.ConsentText) {
		t.Fatal("consent truncated")
	}
	handleClaim(t, h, claimCallback(2, "prize:consent:9:1"))
	handleClaim(t, h, claimMessage(3, "Иван\n"))
	if dialog.dialog.State != "prize:first_name" {
		t.Fatal("control character in name accepted")
	}
	seen := make(map[string]bool)
	for _, key := range queue.keys {
		if seen[key] {
			t.Fatalf("outbox key reused: %s", key)
		}
		seen[key] = true
	}
}
