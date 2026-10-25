package chances

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/entity/participation"
	chancesentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/chances"
	usersession "github.com/letsssgooo/giveaways_bot/internal/handlers/users/session"
	userview "github.com/letsssgooo/giveaways_bot/internal/handlers/users/view"
)

const (
	chancesPagePrefix = "chances:"
	chancesPageSize   = 10
)

func PageAction(data string) (int, bool) {
	value, found := strings.CutPrefix(data, chancesPagePrefix)
	if !found {
		return 0, false
	}
	page, err := strconv.Atoi(value)
	return page, err == nil && page >= 0
}

func ShowChances(ctx context.Context, h *usersession.Session, userID, updateID, chatID int64, page int) error {
	chances, err := h.Promotion.Chances(ctx, userID)
	if err != nil {
		return err
	}
	lastPage := max(0, len(chances.ActiveDays)-1) / chancesPageSize
	page = min(page, lastPage)
	keyboard := &models.InlineKeyboardMarkup{}
	var navigation []models.InlineKeyboardButton
	if page > 0 {
		navigation = append(navigation, models.InlineKeyboardButton{Text: userview.PreviousPageButtonText, CallbackData: chancesPagePrefix + strconv.Itoa(page-1)})
	}
	if page < lastPage {
		navigation = append(navigation, models.InlineKeyboardButton{Text: userview.NextPageButtonText, CallbackData: chancesPagePrefix + strconv.Itoa(page+1)})
	}
	if len(navigation) > 0 {
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, navigation)
	}
	keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, userview.HomeKeyboard().InlineKeyboard...)
	return h.Reply(ctx, userID, updateID, chatID, chancesText(chances, page), keyboard)
}

func chancesText(chances chancesentity.Chances, page int) string {
	parts := []string{fmt.Sprintf(userview.ChancesTextTemplate, chances.Count, participation.MaxFinalChances)}
	if chances.Count == participation.MaxFinalChances {
		parts = append(parts, userview.ChancesLimitText)
	} else {
		parts = append(parts, userview.ChancesRuleText)
	}
	if chances.Configured {
		parts = append(parts, fmt.Sprintf(userview.ChancesRemainingTemplate, chances.RemainingDays))
	} else {
		parts = append(parts, userview.ChancesMissingScheduleText)
	}
	prizes := userview.ChancesMissingPrizeText
	if len(chances.Prizes) > 0 {
		lines := []string{"Главный приз:"}
		for _, prize := range chances.Prizes {
			lines = append(lines, fmt.Sprintf(userview.ChancesPrizeTemplate, prize.Name, prize.PlacesCount, prize.UnitsPerWinner))
		}
		prizes = strings.Join(lines, "\n")
	}
	parts = append(parts, prizes, fmt.Sprintf(userview.ChancesHistoryTemplate, len(chances.ActiveDays)))
	if len(chances.ActiveDays) == 0 {
		return strings.Join(append(parts, userview.ChancesEmptyHistoryText), "\n\n")
	}
	start := page * chancesPageSize
	end := min(start+chancesPageSize, len(chances.ActiveDays))
	var history []string
	for _, day := range chances.ActiveDays[start:end] {
		line := "• " + day.Date.Format("02.01.2006")
		if len(day.Partners) > 0 {
			line += " — " + strings.Join(day.Partners, ", ")
		}
		history = append(history, line)
	}
	parts = append(parts, strings.Join(history, "\n"))
	if len(chances.ActiveDays) > chancesPageSize {
		parts = append(parts, fmt.Sprintf(userview.ChancesHistoryPageTemplate, page+1, (len(chances.ActiveDays)-1)/chancesPageSize+1))
	}
	return strings.Join(parts, "\n\n")
}
