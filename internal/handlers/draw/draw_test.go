package draw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	userentity "github.com/letsssgooo/giveaways_bot/internal/entity/users"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
)

type drawStub struct {
	result       *entity.Result
	approveCount int
	err          error
	list         []entity.Summary
}

func (s *drawStub) List(context.Context, int64) ([]entity.Summary, error) { return s.list, s.err }
func (s *drawStub) View(context.Context, int64, int64) (*entity.Result, error) {
	return s.result, s.err
}
func (s *drawStub) Approve(context.Context, int64, int64) (*entity.Result, error) {
	s.approveCount++
	if s.err != nil {
		return nil, s.err
	}
	now := time.Now()
	s.result.ApprovedAt = &now
	return s.result, nil
}

type testUsers struct{}

func (testUsers) Ensure(_ context.Context, telegramID, chatID int64) (userentity.User, error) {
	return userentity.User{ID: 1, TelegramID: telegramID, ChatID: chatID}, nil
}

type testDialogs struct{ cleared int }

func (s *testDialogs) Clear(context.Context, int64) error { s.cleared++; return nil }

type testOutbox struct {
	messages []json.RawMessage
	keys     []string
}

func (s *testOutbox) Enqueue(_ context.Context, _, _ int64, kind string, payload json.RawMessage, key string) error {
	s.keys = append(s.keys, key)
	if kind == "send_message" {
		s.messages = append(s.messages, payload)
	}
	return nil
}
func testHandler() (*Handler, *drawStub, *testOutbox) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	service := &drawStub{result: &entity.Result{Summary: entity.Summary{ID: 3, Kind: "final", ScheduledAt: now, PerformedAt: &now, ParticipantCount: 12, WinnerCount: 5, PlacesCount: 5, ReserveCount: 5}}}
	outbox := &testOutbox{}
	return New(service, userservice.New(testUsers{}), &testDialogs{}, outbox), service, outbox
}
func callback(data string) *models.Update {
	return &models.Update{ID: 1, CallbackQuery: &models.CallbackQuery{ID: "callback", Data: data, From: models.User{ID: 501}, Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage, Message: &models.Message{Chat: models.Chat{ID: 501, Type: models.ChatTypePrivate}}}}}
}
func handle(t *testing.T, h *Handler, data string) {
	t.Helper()
	handled, err := h.Handle(t.Context(), callback(data))
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}
func lastMessage(t *testing.T, outbox *testOutbox) (string, *models.InlineKeyboardMarkup) {
	t.Helper()
	var value struct {
		Text   string                       `json:"text"`
		Markup *models.InlineKeyboardMarkup `json:"reply_markup"`
	}
	if err := json.Unmarshal(outbox.messages[len(outbox.messages)-1], &value); err != nil {
		t.Fatal(err)
	}
	return value.Text, value.Markup
}
func TestApprovalRequiresSeparateConfirmation(t *testing.T) {
	h, s, outbox := testHandler()
	handle(t, h, "draw:approve:3")
	text, kb := lastMessage(t, outbox)
	if s.approveCount != 0 || !strings.Contains(text, "Утвердить этот результат?") || kb.InlineKeyboard[0][0].CallbackData != "draw:confirm:3" {
		t.Fatalf("unexpected preview: %s %+v count=%d", text, kb, s.approveCount)
	}
	handle(t, h, "draw:confirm:3")
	text, kb = lastMessage(t, outbox)
	if s.approveCount != 1 || !strings.Contains(text, "Утвержден:") {
		t.Fatalf("missing approval: %s", text)
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.CallbackData, "approve:") {
				t.Fatal("approved result can be approved again from UI")
			}
		}
	}
}
func TestApprovalAccessRevokedAfterPreview(t *testing.T) {
	h, s, outbox := testHandler()
	handle(t, h, "draw:approve:3")
	s.err = promotionadminentity.ErrForbidden
	handle(t, h, "draw:confirm:3")
	text, _ := lastMessage(t, outbox)
	if !strings.Contains(text, "недостаточно прав") || s.result.ApprovedAt != nil {
		t.Fatalf("revoked approval: %s", text)
	}
}
func TestMalformedAndNonPrivateCallbacksDoNotApprove(t *testing.T) {
	for _, data := range []string{"draw:", "draw:confirm", "draw:confirm:0", "draw:confirm:-1", "draw:confirm:3:garbage", "draw:results:3:-1", "draw:list:999999999999999999999999"} {
		t.Run(data, func(t *testing.T) {
			h, s, _ := testHandler()
			handle(t, h, data)
			if s.approveCount != 0 {
				t.Fatal("malformed callback approved draw")
			}
		})
	}
	h, s, outbox := testHandler()
	update := callback("draw:confirm:3")
	update.CallbackQuery.Message.Message.Chat.Type = models.ChatTypeGroup
	if handled, err := h.Handle(t.Context(), update); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if s.approveCount != 0 || len(outbox.keys) != 0 {
		t.Fatal("non-private callback has effects")
	}
}
func TestResultsAndParticipantsPagination(t *testing.T) {
	h, s, outbox := testHandler()
	for i := 1; i <= 23; i++ {
		s.result.Participants = append(s.result.Participants, entity.Participant{TelegramID: int64(100 + i), Weight: min(i, 10)})
	}
	handle(t, h, "draw:participants:3:1")
	text, kb := lastMessage(t, outbox)
	if !strings.Contains(text, "Telegram ID 111 — вес 10") || strings.Contains(text, "Telegram ID 110") || strings.Contains(text, "Telegram ID 121") {
		t.Fatal(text)
	}
	if len(kb.InlineKeyboard[0]) != 2 || kb.InlineKeyboard[0][1].CallbackData != "draw:participants:3:2" {
		t.Fatalf("navigation: %+v", kb)
	}
	handle(t, h, "draw:participants:3:999999")
	text, _ = lastMessage(t, outbox)
	if !strings.Contains(text, "Telegram ID 123") || s.approveCount != 0 {
		t.Fatal(text)
	}
	s.result.Winners = []entity.Selection{{TelegramID: 101, Position: 1, PlaceNumber: 1, PrizeName: "Билеты", UnitsPerWinner: 2}}
	s.result.Reserves = []entity.Selection{{TelegramID: 102, Position: 1}}
	handle(t, h, "draw:results:3:0")
	text, _ = lastMessage(t, outbox)
	for _, want := range []string{"Победитель 1: Telegram ID 101", "Билеты — 2 шт.", "Резерв 1: Telegram ID 102"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
}
func TestDrawOperationalErrorPropagates(t *testing.T) {
	h, s, _ := testHandler()
	s.err = fmt.Errorf("database unavailable")
	_, err := h.Handle(t.Context(), callback("draw:view:3"))
	if !errors.Is(err, s.err) {
		t.Fatal(err)
	}
}
