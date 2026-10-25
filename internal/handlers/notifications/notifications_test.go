package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/notifications"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	userentity "github.com/letsssgooo/giveaways_bot/internal/entity/users"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
	userservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
)

type serviceStub struct {
	values                   []entity.Broadcast
	created, sent, cancelled int
	err                      error
}

func (s *serviceStub) Create(_ context.Context, actor, _ int64, audience entity.Audience, text string) (*entity.Broadcast, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.created++
	value := entity.Broadcast{ID: int64(len(s.values) + 1), CreatedBy: actor, Audience: audience, Text: text, CreatedAt: time.Now(), Status: "draft", Total: 3}
	s.values = append(s.values, value)
	return &s.values[len(s.values)-1], nil
}
func (s *serviceStub) View(_ context.Context, _, id int64) (*entity.Broadcast, error) {
	if s.err != nil {
		return nil, s.err
	}
	for i := range s.values {
		if s.values[i].ID == id {
			return &s.values[i], nil
		}
	}
	return nil, promotionadminentity.ErrNotFound
}
func (s *serviceStub) List(context.Context, int64) ([]entity.Broadcast, error) {
	return s.values, s.err
}
func (s *serviceStub) Send(ctx context.Context, actor, id int64) (*entity.Broadcast, error) {
	value, err := s.View(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if value.Status == "cancelled" {
		return nil, promotionadminentity.ErrConflict
	}
	s.sent++
	value.Status = "queued"
	value.Pending = value.Total
	return value, nil
}
func (s *serviceStub) Cancel(ctx context.Context, actor, id int64) (*entity.Broadcast, error) {
	value, err := s.View(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	s.cancelled++
	value.Status = "cancelled"
	return value, nil
}

type adminStub struct {
	access   promotionadminentity.Access
	partners []promotionadminentity.PartnerConfig
	err      error
}

func (s *adminStub) Access(context.Context, int64) (promotionadminentity.Access, error) {
	return s.access, s.err
}
func (s *adminStub) Partners(context.Context, int64) ([]promotionadminentity.PartnerConfig, error) {
	if s.err != nil {
		return nil, s.err
	}
	values := slices.Clone(s.partners)
	if !s.access.Organizer {
		values = slices.DeleteFunc(values, func(v promotionadminentity.PartnerConfig) bool { return !slices.Contains(s.access.PartnerIDs, v.ID) })
	}
	return values, nil
}
func (s *adminStub) AuthorizePartner(_ context.Context, _, id int64) error {
	if s.err != nil {
		return s.err
	}
	if !s.access.Organizer && !slices.Contains(s.access.PartnerIDs, id) {
		return promotionadminentity.ErrForbidden
	}
	if !slices.ContainsFunc(s.partners, func(v promotionadminentity.PartnerConfig) bool { return v.ID == id }) {
		return promotionadminentity.ErrNotFound
	}
	return nil
}

type usersStub struct{}

func (usersStub) Ensure(_ context.Context, telegramID, chatID int64) (userentity.User, error) {
	return userentity.User{ID: 1, TelegramID: telegramID, ChatID: chatID}, nil
}

type dialogsStub struct {
	value *dialogs.Dialog
	ttl   time.Duration
}

func (s *dialogsStub) Load(context.Context, int64) (*dialogs.Dialog, error) { return s.value, nil }
func (s *dialogsStub) Save(_ context.Context, _ int64, state string, payload json.RawMessage, ttl time.Duration) error {
	s.value = &dialogs.Dialog{State: state, Payload: payload}
	s.ttl = ttl
	return nil
}
func (s *dialogsStub) Clear(context.Context, int64) error { s.value = nil; return nil }

type outboxStub struct {
	messages    []json.RawMessage
	keys, kinds []string
}

func (s *outboxStub) Enqueue(_ context.Context, _, _ int64, kind string, payload json.RawMessage, key string) error {
	s.keys = append(s.keys, key)
	s.kinds = append(s.kinds, kind)
	if kind == "send_message" {
		s.messages = append(s.messages, payload)
	}
	return nil
}

type fixture struct {
	handler  *Handler
	service  *serviceStub
	admin    *adminStub
	dialogs  *dialogsStub
	outbox   *outboxStub
	updateID int64
}

func setup() *fixture {
	f := &fixture{service: &serviceStub{}, admin: &adminStub{access: promotionadminentity.Access{Organizer: true}, partners: []promotionadminentity.PartnerConfig{{ID: 7, Name: "Партнер"}, {ID: 9, Name: "Другой"}}}, dialogs: &dialogsStub{}, outbox: &outboxStub{}}
	f.handler = New(f.service, f.admin, userservice.New(usersStub{}), f.dialogs, f.outbox, "test_bot")
	return f
}
func (f *fixture) callback(data string) *models.Update {
	f.updateID++
	return &models.Update{ID: f.updateID, CallbackQuery: &models.CallbackQuery{ID: fmt.Sprint(f.updateID), Data: data, From: models.User{ID: 501}, Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage, Message: &models.Message{Chat: models.Chat{ID: 501, Type: models.ChatTypePrivate}}}}}
}
func (f *fixture) message(text string) *models.Update {
	f.updateID++
	return &models.Update{ID: f.updateID, Message: &models.Message{From: &models.User{ID: 501}, Chat: models.Chat{ID: 501, Type: models.ChatTypePrivate}, Text: text}}
}
func (f *fixture) handle(t *testing.T, update *models.Update, wantHandled bool) {
	t.Helper()
	handled, err := f.handler.Handle(t.Context(), update)
	if err != nil || handled != wantHandled {
		t.Fatalf("handled=%v error=%v", handled, err)
	}
}
func (f *fixture) click(t *testing.T, data string) { t.Helper(); f.handle(t, f.callback(data), true) }
func (f *fixture) write(t *testing.T, text string) { t.Helper(); f.handle(t, f.message(text), true) }
func (f *fixture) last(t *testing.T) (string, *models.InlineKeyboardMarkup) {
	t.Helper()
	var value struct {
		Text   string                       `json:"text"`
		Markup *models.InlineKeyboardMarkup `json:"reply_markup"`
	}
	if len(f.outbox.messages) == 0 {
		t.Fatal("no messages")
	}
	if err := json.Unmarshal(f.outbox.messages[len(f.outbox.messages)-1], &value); err != nil {
		t.Fatal(err)
	}
	return value.Text, value.Markup
}
func (f *fixture) confirmation(t *testing.T) string {
	t.Helper()
	_, kb := f.last(t)
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			if strings.HasPrefix(button.CallbackData, "broadcast:send:") {
				return button.CallbackData
			}
		}
	}
	t.Fatal("missing send confirmation")
	return ""
}
func (f *fixture) draft(t *testing.T) string {
	t.Helper()
	f.click(t, "broadcast:audience:all")
	f.write(t, "Текст рассылки")
	return f.confirmation(t)
}
func TestBroadcastDraftRequiresCurrentConfirmationAndPreservesExactText(t *testing.T) {
	f := setup()
	f.click(t, "broadcast:audience:all")
	if f.dialogs.value.State != "broadcast:text" || f.dialogs.ttl != 30*time.Minute {
		t.Fatal(f.dialogs)
	}
	text := strings.Repeat("🎁", 2048)
	f.write(t, text)
	if f.service.created != 1 || f.service.sent != 0 || f.dialogs.value.State != "broadcast:confirm" {
		t.Fatal(f.service, f.dialogs)
	}
	var preview struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(f.outbox.messages[len(f.outbox.messages)-2], &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Text != text {
		t.Fatal("preview changed or truncated maximum-length text")
	}
	confirm := f.confirmation(t)
	f.click(t, confirm)
	got, _ := f.last(t)
	if f.service.sent != 1 || f.dialogs.value != nil || !strings.Contains(got, "Ожидают отправки: 3") {
		t.Fatal(got, f.service.sent)
	}
	f.click(t, confirm)
	if f.service.sent != 1 {
		t.Fatal("old confirmation sent again")
	}
	seen := map[string]bool{}
	for _, key := range f.outbox.keys {
		if seen[key] {
			t.Fatalf("duplicate outbox key: %s", key)
		}
		seen[key] = true
	}
}
func TestBroadcastNavigationAndCancelInvalidateOldConfirmation(t *testing.T) {
	for _, navigation := range []string{"/cancel", "/menu", "/admin", "menu:home", "admin:home", "broadcast:list:0", "broadcast:new"} {
		t.Run(navigation, func(t *testing.T) {
			f := setup()
			old := f.draft(t)
			if strings.HasPrefix(navigation, "/") {
				f.handle(t, f.message(navigation), navigation == "/cancel")
			} else {
				f.handle(t, f.callback(navigation), strings.HasPrefix(navigation, "broadcast:"))
			}
			f.click(t, old)
			if f.service.sent != 0 {
				t.Fatal("navigation left old confirmation active")
			}
			f.click(t, "broadcast:view:1")
			f.click(t, f.confirmation(t))
			if f.service.sent != 1 {
				t.Fatal("saved draft cannot be reopened")
			}
		})
	}
}
func TestBroadcastNewPreviewInvalidatesOlderDraftButton(t *testing.T) {
	f := setup()
	old := f.draft(t)
	current := f.draft(t)
	f.click(t, old)
	if f.service.sent != 0 {
		t.Fatal("older draft sent")
	}
	f.click(t, current)
	if f.service.sent != 1 || f.service.values[0].Status != "draft" || f.service.values[1].Status != "queued" {
		t.Fatal(f.service)
	}
}
func TestBroadcastCancelDraftPreventsOldSend(t *testing.T) {
	f := setup()
	old := f.draft(t)
	f.click(t, "broadcast:cancel:1")
	f.click(t, old)
	if f.service.cancelled != 1 || f.service.sent != 0 || f.service.values[0].Status != "cancelled" {
		t.Fatal(f.service)
	}
}
func TestBroadcastExplicitIDsAndInvalidInput(t *testing.T) {
	f := setup()
	f.click(t, "broadcast:audience:users")
	f.write(t, "123, -4")
	if f.dialogs.value.State != "broadcast:users" {
		t.Fatal("invalid list advanced flow")
	}
	f.write(t, "123, 456\n123;789")
	f.write(t, "Выбранным пользователям")
	if !slices.Equal(f.service.values[0].Audience.TelegramIDs, []int64{123, 456, 789}) {
		t.Fatal(f.service.values[0])
	}
}
func TestBroadcastPartnerRolesAndRevocation(t *testing.T) {
	f := setup()
	f.admin.partners[0].Name = "Кофейня на площади"
	f.admin.access = promotionadminentity.Access{PartnerIDs: []int64{7}}
	f.click(t, "broadcast:new")
	_, kb := f.last(t)
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "broadcast:audience:all" || b.CallbackData == "broadcast:audience:users" {
				t.Fatal("organizer audience exposed")
			}
		}
	}
	for _, action := range []string{"broadcast:audience:all", "broadcast:audience:users", "broadcast:partner:9"} {
		f.click(t, action)
		text, _ := f.last(t)
		if !strings.Contains(text, "недостаточно прав") || f.dialogs.value != nil {
			t.Fatal(text)
		}
	}
	f.click(t, "broadcast:partner:7")
	f.admin.access = promotionadminentity.Access{}
	f.write(t, "После отзыва прав")
	if f.service.created != 0 {
		t.Fatal("revoked admin created draft")
	}
	f.admin.access = promotionadminentity.Access{PartnerIDs: []int64{7}}
	f.write(t, "Для участников партнера")
	if f.service.values[0].Audience.PartnerID != 7 {
		t.Fatal(f.service.values[0])
	}
	text, _ := f.last(t)
	if !strings.Contains(text, "Аудитория: участники партнера «Кофейня на площади»") {
		t.Fatalf("partner name missing from preview: %s", text)
	}
	confirm := f.confirmation(t)
	f.admin.access = promotionadminentity.Access{}
	f.click(t, confirm)
	if f.service.sent != 0 {
		t.Fatal("revoked admin sent draft")
	}
}
func TestBroadcastOperationalErrorsPropagateAndInputValidationKeepsFlow(t *testing.T) {
	f := setup()
	f.click(t, "broadcast:audience:all")
	f.service.err = fmt.Errorf("%w: текст слишком длинный", promotionadminentity.ErrInvalid)
	f.write(t, "Invalid")
	if f.dialogs.value.State != "broadcast:text" || f.service.created != 0 {
		t.Fatal(f.dialogs, f.service)
	}
	f.service.err = errors.New("database unavailable")
	if _, err := f.handler.Handle(t.Context(), f.message("retry")); !errors.Is(err, f.service.err) {
		t.Fatal(err)
	}
}
func TestBroadcastDoesNotInterceptOtherDialogsOrAddressedCommands(t *testing.T) {
	f := setup()
	f.dialogs.value = &dialogs.Dialog{State: "admin:input", Payload: json.RawMessage(`{}`)}
	f.handle(t, f.message("Текст для админки"), false)
	f.handle(t, f.message("/cancel"), false)
	if f.dialogs.value == nil {
		t.Fatal("other dialog cleared")
	}
	f.click(t, "broadcast:audience:all")
	f.handle(t, f.message("/cancel@other_bot"), false)
	if f.dialogs.value == nil {
		t.Fatal("foreign command cleared dialog")
	}
	f.handle(t, f.message("/cancel@test_bot"), true)
	if f.dialogs.value != nil {
		t.Fatal("own cancel ignored")
	}
}
func TestBroadcastPaginationAndMalformedCallbacks(t *testing.T) {
	f := setup()
	for i := 1; i <= 23; i++ {
		f.service.values = append(f.service.values, entity.Broadcast{ID: int64(i), Status: "draft"})
	}
	f.click(t, "broadcast:list:1")
	_, kb := f.last(t)
	if kb.InlineKeyboard[1][0].CallbackData != "broadcast:view:11" || kb.InlineKeyboard[10][0].CallbackData != "broadcast:view:20" {
		t.Fatal(kb)
	}
	f.click(t, "broadcast:list:999999")
	_, kb = f.last(t)
	if kb.InlineKeyboard[1][0].CallbackData != "broadcast:view:21" {
		t.Fatal(kb)
	}
	for _, data := range []string{"broadcast:", "broadcast:send:1", "broadcast:send:-1:0", "broadcast:send:1:0:extra", "broadcast:list:-1", "broadcast:audience:other", "broadcast:view:0"} {
		f.click(t, data)
	}
	if f.service.sent != 0 {
		t.Fatal("malformed callback sent")
	}
}
func TestBroadcastPrivateChatAndInaccessibleCallback(t *testing.T) {
	for _, change := range []func(*models.Update){
		func(u *models.Update) { u.CallbackQuery.From.IsBot = true },
		func(u *models.Update) { u.CallbackQuery.Message.Message.Chat.Type = models.ChatTypeGroup },
		func(u *models.Update) { u.CallbackQuery.Message.Message.Chat.ID = 999 },
	} {
		f := setup()
		u := f.callback("broadcast:list:0")
		change(u)
		f.handle(t, u, true)
		if len(f.outbox.keys) != 0 {
			t.Fatal("non-private callback has effects")
		}
	}
	f := setup()
	u := f.callback("broadcast:list:0")
	u.CallbackQuery.Message = models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeInaccessibleMessage, InaccessibleMessage: &models.InaccessibleMessage{Chat: models.Chat{ID: 501, Type: models.ChatTypePrivate}}}
	f.handle(t, u, true)
	text, _ := f.last(t)
	if !strings.Contains(text, "устарела") || f.outbox.kinds[0] != "answer_callback_query" {
		t.Fatal(text, f.outbox.kinds)
	}
}

func TestBroadcastCompletedDeliveryShowsFinalCounts(t *testing.T) {
	f := setup()
	f.service.values = []entity.Broadcast{{ID: 1, Status: "queued", Text: "Новости", Total: 3, Sent: 2, Failed: 1}}
	f.click(t, "broadcast:view:1")
	text, _ := f.last(t)
	for _, want := range []string{"Статус: завершена", "Ожидают отправки: 0", "Доставлено: 2", "Не доставлено: 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	f.click(t, "broadcast:list:0")
	_, kb := f.last(t)
	if !strings.Contains(kb.InlineKeyboard[1][0].Text, "завершена") {
		t.Fatal(kb)
	}
}
