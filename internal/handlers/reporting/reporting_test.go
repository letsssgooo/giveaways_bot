package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/reporting"
	userentity "github.com/letsssgooo/giveaways_bot/internal/entity/users"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

type reportStub struct {
	stats      entity.Stats
	err        error
	authorized int
	partners   []entity.Partner
}

func (s *reportStub) Stats(context.Context, int64, int64) (entity.Stats, error) {
	return s.stats, s.err
}
func (s *reportStub) Partners(context.Context, int64) ([]entity.Partner, error) {
	return s.partners, s.err
}
func (s *reportStub) Authorize(context.Context, int64, int64) error { s.authorized++; return s.err }

type testUsers struct{}

func (testUsers) Ensure(_ context.Context, actor, chat int64) (userentity.User, error) {
	return userentity.User{ID: 1, TelegramID: actor, ChatID: chat}, nil
}

type testDialogs struct{}

func (testDialogs) Clear(context.Context, int64) error { return nil }

type recordedMessage struct {
	kind    string
	payload json.RawMessage
	key     string
}
type testOutbox struct{ messages []recordedMessage }

func (o *testOutbox) Enqueue(_ context.Context, _, _ int64, kind string, payload json.RawMessage, key string) error {
	o.messages = append(o.messages, recordedMessage{kind, payload, key})
	return nil
}
func newTestHandler() (*Handler, *reportStub, *testOutbox) {
	s := &reportStub{}
	o := &testOutbox{}
	return New(s, userservice.New(testUsers{}), testDialogs{}, o), s, o
}
func callback(data string) *models.Update {
	return &models.Update{ID: 10, CallbackQuery: &models.CallbackQuery{ID: "cb", From: models.User{ID: 501}, Data: data, Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage, Message: &models.Message{Chat: models.Chat{ID: 501, Type: models.ChatTypePrivate}}}}}
}
func handle(t *testing.T, h *Handler, data string) {
	t.Helper()
	handled, err := h.Handle(t.Context(), callback(data))
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}
func (o *testOutbox) reports() []recordedMessage {
	var rows []recordedMessage
	for _, m := range o.messages {
		if m.kind == protocol.MessageKindReport {
			rows = append(rows, m)
		}
	}
	return rows
}
func (o *testOutbox) text() string {
	var text string
	for _, m := range o.messages {
		if m.kind == protocol.MessageKindText {
			var p struct{ Text string }
			_ = json.Unmarshal(m.payload, &p)
			text += p.Text
		}
	}
	return text
}
func TestExportQueuesOnlyAuthenticatedRecipientAndParameters(t *testing.T) {
	h, s, o := newTestHandler()
	handle(t, h, "report:export:winners:7")
	reports := o.reports()
	if len(reports) != 1 || s.authorized != 1 {
		t.Fatal("missing scoped authorization or export")
	}
	var request protocol.ReportRequest
	if err := json.Unmarshal(reports[0].payload, &request); err != nil {
		t.Fatal(err)
	}
	if request.ActorID != 501 || request.ChatID != 501 || request.PartnerID != 7 || request.Kind != "winners" {
		t.Fatalf("wrong export request %+v", request)
	}
	if len(o.messages) != 3 || o.messages[0].kind != protocol.MessageKindCallbackAnswer || o.messages[1].kind != protocol.MessageKindText || o.messages[2].kind != protocol.MessageKindReport {
		t.Fatal("queue acknowledgement must precede file")
	}
}
func TestUnauthorizedOrMalformedExportDoesNotQueueDocument(t *testing.T) {
	for _, data := range []string{"report:", "report:export:winners:-1", "report:export:unknown:0", "report:export:winners:0:999", "report:home:-1", "report:scope:999999999999999999999"} {
		t.Run(data, func(t *testing.T) {
			h, _, o := newTestHandler()
			handle(t, h, data)
			if len(o.reports()) != 0 {
				t.Fatal("malformed request exported data")
			}
		})
	}
	h, s, o := newTestHandler()
	s.err = promotionadminentity.ErrForbidden
	handle(t, h, "report:export:winners:7")
	if len(o.reports()) != 0 || !strings.Contains(o.text(), "недостаточно прав") {
		t.Fatal("unauthorized export not rejected")
	}
}
func TestReportingRejectsGroupAndWrongPrivateRecipient(t *testing.T) {
	for _, chat := range []models.Chat{{ID: -123, Type: models.ChatTypeGroup}, {ID: 502, Type: models.ChatTypePrivate}} {
		h, s, o := newTestHandler()
		u := callback("report:export:winners:0")
		u.CallbackQuery.Message.Message.Chat = chat
		handled, err := h.Handle(t.Context(), u)
		if !handled || err != nil || len(o.messages) != 0 || s.authorized != 0 {
			t.Fatalf("invalid chat has effects: %v %v", handled, err)
		}
	}
}
func TestPartnerStatisticsNeverRenderGlobalUsers(t *testing.T) {
	h, s, o := newTestHandler()
	s.stats = entity.Stats{TotalUsers: 987654, GlobalUsersVisible: false, PromoRequests: 3, PromoIssued: 2, PromoRemaining: 4, Registrations: 1}
	handle(t, h, "report:home:0")
	if strings.Contains(o.text(), "987654") || !strings.Contains(o.text(), "Запросов промокода: 3") {
		t.Fatal(o.text())
	}
	handle(t, h, "report:users:0:0")
	if !strings.Contains(o.text(), "недостаточно прав") {
		t.Fatal("forged global users button allowed")
	}
}
func TestReportingPaginationClampsOldPageAndPreservesScope(t *testing.T) {
	h, s, o := newTestHandler()
	for i := 1; i <= 12; i++ {
		s.stats.Partners = append(s.stats.Partners, entity.PartnerStats{Partner: entity.Partner{ID: int64(i), Name: fmt.Sprintf("Partner %02d", i)}})
	}
	handle(t, h, "report:partners:7:999999")
	text := o.text()
	if !strings.Contains(text, "Partner 12") || strings.Contains(text, "Partner 01") {
		t.Fatal(text)
	}
	var payload struct {
		Markup *models.InlineKeyboardMarkup `json:"reply_markup"`
	}
	if err := json.Unmarshal(o.messages[len(o.messages)-1].payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Markup.InlineKeyboard[0][0].CallbackData != "report:partners:7:0" {
		t.Fatal("pagination lost partner scope")
	}
}
