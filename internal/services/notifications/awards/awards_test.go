package awards

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

func TestAwardMessagesKeepInstructionsWithinTelegramLimit(t *testing.T) {
	award := awardsentity.Award{ID: 17, TelegramID: 501, Kind: "final", PrizeName: "Два билета", Units: 2,
		ResponseHours: 24, Instructions: strings.Repeat("🎫 Инструкция. ", 900)}
	parts, err := AwardMessages(award)
	if err != nil || len(parts) < 2 {
		t.Fatalf("parts=%d err=%v", len(parts), err)
	}
	var combined strings.Builder
	for i, part := range parts {
		var payload struct {
			ChatID int64                        `json:"chat_id"`
			Text   string                       `json:"text"`
			Markup *models.InlineKeyboardMarkup `json:"reply_markup"`
		}
		if err := json.Unmarshal(part, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ChatID != 501 || len(utf16.Encode([]rune(payload.Text))) > protocol.MaxMessageLength {
			t.Fatalf("invalid message part %d", i)
		}
		combined.WriteString(payload.Text)
		if i < len(parts)-1 && payload.Markup != nil {
			t.Fatal("claim button precedes complete instructions")
		}
		if i == len(parts)-1 && (payload.Markup == nil || payload.Markup.InlineKeyboard[0][0].CallbackData != "prize:claim:17") {
			t.Fatal("missing claim button")
		}
	}
	if !strings.Contains(combined.String(), award.Instructions) || !strings.Contains(combined.String(), "24 ч. после доставки") {
		t.Fatal("instructions or claim deadline lost")
	}
}
