package codes

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/entity/participation"
	usersession "github.com/letsssgooo/giveaways_bot/internal/handlers/users/session"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
)

const (
	codePagePrefix = "codes:"
	PageSize       = 10
)

func PageAction(data string) (int, bool) {
	value, found := strings.CutPrefix(data, codePagePrefix)
	if !found {
		return 0, false
	}
	page, err := strconv.Atoi(value)
	return page, err == nil && page >= 0
}

func ChooseCode(ctx context.Context, h *usersession.Session, userID, updateID, chatID int64, page int, selectSingle bool) error {
	options, err := h.Participation.CodeOptions(ctx, userID)
	if err != nil {
		return err
	}
	if len(options) == 0 {
		return h.Reply(ctx, userID, updateID, chatID, userview.NoCodesText, userview.HomeKeyboard())
	}
	if len(options) == 1 && selectSingle {
		return RequestCode(ctx, h, userID, updateID, chatID, options[0].PartnerDayID)
	}
	page = min(page, (len(options)-1)/PageSize)
	start := page * PageSize
	end := min(start+PageSize, len(options))
	keyboard := &models.InlineKeyboardMarkup{}
	for _, option := range options[start:end] {
		label := fmt.Sprintf(userview.CodeOptionTextTemplate, option.PartnerName, option.Date.Format("02.01.2006"))
		if option.HasCode {
			label += userview.ReceivedCodeSuffix
		}
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, []models.InlineKeyboardButton{{
			Text: label, CallbackData: fmt.Sprintf("%s:%s:%d", userview.PartnerActionGroup, userview.PartnerActionCode, option.PartnerDayID),
		}})
	}
	var navigation []models.InlineKeyboardButton
	if page > 0 {
		navigation = append(navigation, models.InlineKeyboardButton{Text: userview.PreviousPageButtonText, CallbackData: codePagePrefix + strconv.Itoa(page-1)})
	}
	if end < len(options) {
		navigation = append(navigation, models.InlineKeyboardButton{Text: userview.NextPageButtonText, CallbackData: codePagePrefix + strconv.Itoa(page+1)})
	}
	if len(navigation) > 0 {
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, navigation)
	}
	keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, userview.HomeKeyboard().InlineKeyboard...)
	return h.Reply(ctx, userID, updateID, chatID, userview.ChooseCodeText, keyboard)
}

func RequestCode(ctx context.Context, h *usersession.Session, userID, updateID, chatID, dayID int64) error {
	result, err := h.Participation.RequestCode(ctx, userID, dayID, updateID)
	if err != nil {
		return err
	}
	switch result.Outcome {
	case participation.CodeIssued, participation.CodeRepeated:
		text, err := codeText(result)
		if err != nil {
			return err
		}
		keyboard := userview.HomeKeyboard()
		if result.Outcome == participation.CodeIssued {
			keyboard = codeJoinKeyboard(dayID)
		}
		return h.Reply(ctx, userID, updateID, chatID, text, keyboard)
	case participation.CodeExhausted:
		return h.Reply(ctx, userID, updateID, chatID, fmt.Sprintf(userview.ExhaustedCodesTextTemplate, result.PartnerName), codeJoinKeyboard(dayID))
	case participation.CodeClosed, participation.CodeUnavailable:
		return h.Reply(ctx, userID, updateID, chatID, userview.ClosedPartnerText, userview.HomeKeyboard())
	default:
		return fmt.Errorf("unknown promo code outcome: %q", result.Outcome)
	}
}

func codeJoinKeyboard(dayID int64) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: userview.JoinButtonText, CallbackData: fmt.Sprintf("%s:%s:%d", userview.PartnerActionGroup, userview.PartnerActionJoin, dayID)}},
		userview.HomeKeyboard().InlineKeyboard[0],
	}}
}

func codeText(result participation.CodeResult) (string, error) {
	text := fmt.Sprintf(userview.IssuedCodeTextTemplate, result.PartnerName, result.Code)
	if result.Outcome == participation.CodeRepeated {
		text = fmt.Sprintf(userview.RepeatedCodeTextTemplate, result.PartnerName, result.Date.Format("02.01.2006"), result.Code)
	}
	if result.ValidUntil != nil {
		location, err := time.LoadLocation("Europe/Moscow")
		if err != nil {
			return "", fmt.Errorf("load promotion time zone: %w", err)
		}
		text += "\n\n" + fmt.Sprintf(userview.CodeValidUntilTextTemplate, result.ValidUntil.In(location).Format("02.01.2006 15:04"))
	}
	instructions := result.Instructions
	if strings.TrimSpace(instructions) == "" {
		instructions = userview.MissingCodeInstructions
	}
	text += "\n\n" + instructions
	if result.Outcome == participation.CodeIssued {
		text += "\n\n" + userview.CodeParticipationText
	}
	text += "\n\n" + fmt.Sprintf(userview.CodeChancesTextTemplate, result.Chances, participation.MaxFinalChances)
	return text, nil
}
