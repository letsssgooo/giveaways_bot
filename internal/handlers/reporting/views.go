package reporting

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
)

const pageSize = 10

func bounds(page, total int) (int, int, int) {
	page = min(page, max(0, (total-1)/pageSize))
	start := page * pageSize
	return page, start, min(start+pageSize, total)
}
func addPages(markup *models.InlineKeyboardMarkup, prefix string, page, end, total int) {
	var row []models.InlineKeyboardButton
	if page > 0 {
		row = append(row, button("← Назад", fmt.Sprintf("%s%d", prefix, page-1)))
	}
	if end < total {
		row = append(row, button("Далее →", fmt.Sprintf("%s%d", prefix, page+1)))
	}
	if len(row) > 0 {
		markup.InlineKeyboard = append(markup.InlineKeyboard, row)
	}
}
func (h *Handler) home(ctx context.Context, r request, partner int64) error {
	stats, err := h.service.Stats(ctx, r.actorID, partner)
	if err != nil {
		return err
	}
	title := "Статистика доступных партнеров"
	if stats.GlobalUsersVisible {
		title = "Статистика всей акции"
	} else if partner > 0 && len(stats.Partners) == 1 {
		title = "Статистика: " + stats.Partners[0].Name
	}
	text := title + "\nПоказатели за всё время; даты — московские."
	if stats.GlobalUsersVisible {
		text += fmt.Sprintf("\n\nПользователей, запустивших бота: %d", stats.TotalUsers)
	}
	text += fmt.Sprintf("\n\nЗапросов промокода: %d\nВыдано кодов: %d\nОсталось кодов: %d\nРегистраций в ежедневных розыгрышах: %d", stats.PromoRequests, stats.PromoIssued, stats.PromoRemaining, stats.Registrations)
	text += "\n\nПовторное нажатие считается запросом, повторная доставка того же события — нет. Один человек может зарегистрироваться в нескольких розыгрышах."
	buttons := []models.InlineKeyboardButton{button("Выбрать партнера", "scope:0"), button("Промокоды по партнерам", fmt.Sprintf("partners:%d:0", partner)), button("Участники по дням", fmt.Sprintf("days:%d:0", partner))}
	if stats.GlobalUsersVisible {
		buttons = append(buttons, button("Новые пользователи по дням", "users:0:0"))
	}
	buttons = append(buttons, button("Участники · CSV", fmt.Sprintf("export:participants:%d", partner)), button("Победители и резерв · CSV", fmt.Sprintf("export:winners:%d", partner)), back().InlineKeyboard[0][0])
	return h.reply(ctx, r, text, keyboard(buttons...))
}
func (h *Handler) scopes(ctx context.Context, r request, page int) error {
	partners, err := h.service.Partners(ctx, r.actorID)
	if err != nil {
		return err
	}
	page, start, end := bounds(page, len(partners))
	markup := keyboard(button("Все доступные партнеры", "home:0"))
	for _, partner := range partners[start:end] {
		markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{button(partner.Name, fmt.Sprintf("home:%d", partner.ID))})
	}
	addPages(markup, "scope:", page, end, len(partners))
	markup.InlineKeyboard = append(markup.InlineKeyboard, back().InlineKeyboard...)
	return h.reply(ctx, r, "Выберите область статистики и выгрузок.", markup)
}
func (h *Handler) details(ctx context.Context, r request, partner int64, kind string, page int) error {
	stats, err := h.service.Stats(ctx, r.actorID, partner)
	if err != nil {
		return err
	}
	var lines []string
	title := ""
	switch kind {
	case "partners":
		title = "Промокоды по партнерам"
		for _, p := range stats.Partners {
			lines = append(lines, fmt.Sprintf("%s\nЗапросов: %d · выдано: %d · остаток: %d", p.Name, p.Requests, p.Issued, p.Remaining))
		}
	case "days":
		title = "Участники ежедневных розыгрышей"
		for _, d := range stats.Days {
			lines = append(lines, fmt.Sprintf("%s · %s\nУчастников: %d", d.Date.Format("02.01.2006"), d.PartnerName, d.Registrations))
		}
	case "users":
		if !stats.GlobalUsersVisible {
			return promotionadminentity.ErrForbidden
		}
		title = "Новые пользователи по московским датам"
		for _, d := range stats.NewUsers {
			lines = append(lines, fmt.Sprintf("%s: %d", d.Date.Format("02.01.2006"), d.Count))
		}
	}
	page, start, end := bounds(page, len(lines))
	text := title
	if len(lines) == 0 {
		text += "\nДанных пока нет."
	} else {
		text += "\n\n" + strings.Join(lines[start:end], "\n\n")
	}
	markup := keyboard()
	addPages(markup, fmt.Sprintf("%s:%d:", kind, partner), page, end, len(lines))
	markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{button("К статистике", fmt.Sprintf("home:%d", partner))})
	return h.reply(ctx, r, text, markup)
}
