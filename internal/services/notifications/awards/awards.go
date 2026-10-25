package awards

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

// AwardMessages готовит уведомление для существующей очереди отправки.
// Кнопка находится в последней части: срок от доставки начинается после всех частей.
func AwardMessages(award awardsentity.Award) ([]json.RawMessage, error) {
	heading := "Ты в числе хедлайнеров дня 🔥"
	if award.Kind == "final" {
		heading = "Ты победил в финальном розыгрыше 🔥"
	}
	prize := fmt.Sprintf("Ты выиграл «%s» — %d шт.", award.PrizeName, award.Units)
	if award.PartnerName != "" {
		prize += "\nПартнер: " + award.PartnerName
	}
	text := heading + "\n\n" + prize
	if strings.TrimSpace(award.Instructions) != "" {
		text += "\n\n" + award.Instructions
	}
	text += "\n\nНажми «Получить приз», ознакомься с согласием и укажи имя и фамилию."
	if award.ResponseStartsOn == "approved" {
		// Администратор может возобновить доставку с новым сроком. Сохраненное
		// уведомление ведет к актуальной дате, которую показывает экран получения.
		text += "\nСрок подтверждения уже отсчитывается. Нажми «Получить приз», чтобы увидеть актуальную дату."
	} else {
		text += fmt.Sprintf("\nНа подтверждение — %d ч. после доставки уведомления.", award.ResponseHours)
	}
	text += "\nВернуться к своим призам: /prizes."
	parts := splitAwardText(text)
	messages := make([]json.RawMessage, 0, len(parts))
	for i, part := range parts {
		payload := struct {
			ChatID int64                        `json:"chat_id"`
			Text   string                       `json:"text"`
			Markup *models.InlineKeyboardMarkup `json:"reply_markup,omitempty"`
		}{ChatID: award.TelegramID, Text: part}
		if i == len(parts)-1 {
			payload.Markup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{
				Text: "Получить приз", CallbackData: fmt.Sprintf("prize:claim:%d", award.ID),
			}}}}
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		messages = append(messages, encoded)
	}
	return messages, nil
}

func splitAwardText(text string) []string {
	var parts []string
	start, length := 0, 0
	for i, char := range text {
		size := utf16.RuneLen(char)
		if length+size > protocol.MaxMessageLength {
			parts = append(parts, text[start:i])
			start, length = i, 0
		}
		length += size
	}
	return append(parts, text[start:])
}
