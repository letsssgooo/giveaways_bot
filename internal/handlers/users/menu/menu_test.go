package menu_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	registrationentity "github.com/letsssgooo/giveaways_bot/internal/entity/participation/registration"
	entity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion"
	chancesentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/chances"
	usershandler "github.com/letsssgooo/giveaways_bot/internal/handlers/users"
	testutil "github.com/letsssgooo/giveaways_bot/internal/handlers/users/internal/testutil"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
	promotionService "github.com/letsssgooo/giveaways_bot/internal/services/promotion"
)

func TestStartBannerAndFiveActions(t *testing.T) {
	for _, banner := range []string{"", "telegram-file-id"} {
		handler, _, queue := testutil.NewHandler()
		handler.Promotion = promotionService.New(&testutil.PromotionRepository{Promotion: &entity.Promotion{BannerRef: banner}})
		if err := handler.Handle(t.Context(), testutil.PrivateMessage("/start")); err != nil {
			t.Fatal(err)
		}
		if len(queue.Messages) != 2 || queue.Messages[0].Kind != "send_photo" || queue.Messages[0].Key != "telegram:update:42:banner" {
			t.Fatalf("missing separate banner: %+v", queue.Messages)
		}
		var photo struct {
			ChatID int64  `json:"chat_id"`
			Photo  string `json:"photo"`
		}
		if err := json.Unmarshal(queue.Messages[0].Payload, &photo); err != nil {
			t.Fatal(err)
		}
		if banner == "" {
			banner = "builtin:welcome"
		}
		if photo.ChatID != 101 || photo.Photo != banner {
			t.Fatalf("unexpected photo: %+v", photo)
		}
		reply := testutil.LastReply(t, queue)
		want := []string{"Получить промокод", "Участвовать в розыгрыше", "Как участвовать", "Мои шансы", "Правила акции"}
		if reply.Text != userview.WelcomeText || reply.ReplyMarkup == nil || len(reply.ReplyMarkup.InlineKeyboard) != len(want) {
			t.Fatalf("unexpected menu: %+v", reply)
		}
		for i, row := range reply.ReplyMarkup.InlineKeyboard {
			if len(row) != 1 || row[0].Text != want[i] || !usershandler.KnownAction(row[0].CallbackData) {
				t.Fatalf("unexpected button: %+v", row)
			}
		}
	}
}

func TestJoinPartnerSelectionAndStaleButtons(t *testing.T) {
	for _, action := range []string{"join"} {
		for _, count := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/%d", action, count), func(t *testing.T) {
				handler, _, queue := testutil.NewHandler()
				repository := &testutil.PromotionRepository{}
				for i := range count {
					repository.Partners = append(repository.Partners, entity.PartnerDay{ID: int64(i + 1), Name: fmt.Sprintf("Партнер %d", i+1)})
				}
				handler.Promotion = promotionService.New(repository)
				handler.WithRegistration(&testutil.RegistrationStub{Day: func(dayID int64) *registrationentity.RegistrationDay {
					for _, partner := range repository.Partners {
						if partner.ID == dayID {
							return &registrationentity.RegistrationDay{ID: dayID, Name: partner.Name, Open: true, SubscriptionURL: "https://t.me/partner"}
						}
					}
					return nil
				}}, registrationentity.Verification{})
				if err := handler.Handle(t.Context(), testutil.PrivateCallback("menu:"+action)); err != nil {
					t.Fatal(err)
				}
				reply := testutil.LastReply(t, queue)
				switch count {
				case 0:
					if reply.Text != userview.NoPartnersText {
						t.Fatal(reply.Text)
					}
				case 1:
					if !strings.Contains(reply.Text, "Партнер 1") || !strings.Contains(reply.Text, "подпишись") {
						t.Fatal(reply.Text)
					}
				case 2:
					buttons := reply.ReplyMarkup.InlineKeyboard
					if len(buttons) != 3 || buttons[1][0].CallbackData != "partner:"+action+":2" {
						t.Fatalf("unexpected partners: %+v", buttons)
					}
					if err := handler.Handle(t.Context(), testutil.PrivateCallback(buttons[1][0].CallbackData)); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(testutil.LastReply(t, queue).Text, "Партнер 2") {
						t.Fatal("wrong selected partner")
					}
				}
				// Между показом выбора и нажатием прием закрылся.
				repository.Partners = nil
				if err := handler.Handle(t.Context(), testutil.PrivateCallback("partner:"+action+":1")); err != nil {
					t.Fatal(err)
				}
				if testutil.LastReply(t, queue).Text != userview.ClosedPartnerText {
					t.Fatal("stale partner was accepted")
				}
			})
		}
	}
}

func TestMaterialsChancesAndNavigation(t *testing.T) {
	for _, tc := range []struct{ action, want string }{
		{"menu:how", "Инструкция из БД"}, {"menu:rules", "Правила из БД"},
		{"menu:chances", "Накоплено шансов: 10 из 10"}, {"menu:home", userview.WelcomeText},
	} {
		t.Run(tc.action, func(t *testing.T) {
			handler, _, queue := testutil.NewHandler()
			handler.Promotion = promotionService.New(&testutil.PromotionRepository{Promotion: &entity.Promotion{Rules: "Правила из БД", Instructions: "Инструкция из БД"}, ChancesData: chancesentity.Chances{ActiveDays: make([]chancesentity.ActivityDay, 15)}})
			if err := handler.Handle(t.Context(), testutil.PrivateCallback(tc.action)); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(testutil.LastReply(t, queue).Text, tc.want) {
				t.Fatal(testutil.LastReply(t, queue).Text)
			}
		})
	}
	for _, tc := range []struct{ action, want string }{{"menu:rules", userview.MissingRulesText}, {"menu:how", fmt.Sprintf(userview.DefaultInstructionsTemplate, 10)}} {
		handler, _, queue := testutil.NewHandler()
		if err := handler.Handle(t.Context(), testutil.PrivateCallback(tc.action)); err != nil {
			t.Fatal(err)
		}
		if testutil.LastReply(t, queue).Text != tc.want {
			t.Fatal(testutil.LastReply(t, queue).Text)
		}
	}
}

func TestLongRulesSplitWithoutLosingText(t *testing.T) {
	handler, _, queue := testutil.NewHandler()
	rules := strings.Repeat("Правила 🎁\n", 1000)
	handler.Promotion = promotionService.New(&testutil.PromotionRepository{Promotion: &entity.Promotion{Rules: rules}})
	if err := handler.Handle(t.Context(), testutil.PrivateCallback("menu:rules")); err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	keys := map[string]bool{}
	for i, message := range queue.Messages[1:] {
		var part testutil.MessageReply
		if err := json.Unmarshal(message.Payload, &part); err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(part.Text) || len(utf16.Encode([]rune(part.Text))) > 4096 || part.Text == "" {
			t.Fatal("invalid message part")
		}
		if (part.ReplyMarkup != nil) != (i == len(queue.Messages)-2) {
			t.Fatal("keyboard must be on final part")
		}
		if keys[message.Key] {
			t.Fatal("duplicate outbox key")
		}
		keys[message.Key] = true
		joined.WriteString(part.Text)
	}
	if joined.String() != rules {
		t.Fatal("rules changed during splitting")
	}
}

func TestMenuReadErrorsAreRetried(t *testing.T) {
	want := errors.New("database unavailable")
	for _, action := range []string{"menu:rules", "menu:join", "menu:chances"} {
		handler, _, _ := testutil.NewHandler()
		handler.Promotion = promotionService.New(&testutil.PromotionRepository{Err: want})
		if err := handler.Handle(t.Context(), testutil.PrivateCallback(action)); !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
	}
}
