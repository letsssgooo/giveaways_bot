// Package navigation показывает меню управления акцией и обрабатывает переходы.
package navigation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	forms "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/forms"
	session "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/session"
	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
)

type Handler struct{ *session.Session }

func (h *Handler) Home(ctx context.Context, r session.Request) error {
	access, err := h.Access(ctx, r)
	if err != nil {
		return err
	}
	buttons := []models.InlineKeyboardButton{session.Button("Партнеры, расписание и промокоды", "partners")}
	buttons = append(buttons, models.InlineKeyboardButton{Text: "Статистика и выгрузки", CallbackData: "report:home:0"})
	buttons = append(buttons, models.InlineKeyboardButton{Text: "Рассылки", CallbackData: "broadcast:list:0"})
	buttons = append(buttons, models.InlineKeyboardButton{Text: "Получение призов: ручной разбор", CallbackData: "prizeadmin:list"})
	buttons = append(buttons, models.InlineKeyboardButton{Text: "Результаты розыгрышей", CallbackData: "draw:list:0"})
	text := "Админка партнера. Доступны только назначенные вам партнеры."
	if access.Organizer {
		text = "Админка организатора. Все даты и время — московские."
		buttons = append(buttons, session.Button("Акция", "campaign"), session.Button("Материалы", "materials"), session.Button("Получение призов", "policies"), session.Button("Финальный розыгрыш", "final"), session.Button("Доступы", "grants"))
		buttons = append(buttons, session.Button("Резерв финала", "final_reserve"))
	}
	return h.Reply(ctx, r, text, session.Keyboard(buttons...))
}

func (h *Handler) Callback(ctx context.Context, r session.Request, data string) error {
	access, err := h.Access(ctx, r)
	if err != nil {
		return err
	}
	parts := strings.Split(data, ":")
	action := parts[0]
	if action == "confirm" && len(parts) == 2 {
		return h.confirmCallback(ctx, r, parts[1])
	}
	if err := h.Dialogs.Clear(ctx, r.UserID); err != nil {
		return err
	}
	if len(parts) == 1 {
		return h.menuCallback(ctx, r, access, action)
	}
	if action == "material" && len(parts) == 2 {
		if !access.Organizer {
			return promotionadmin.ErrForbidden
		}
		labels := map[string]string{"rules": "правила акции", "instructions": "инструкцию по участию", "banner": "фото баннера, Telegram file_id или публичную HTTP(S)-ссылку; «-» возвращает встроенный баннер", "consent": "версию согласия в первой строке (например 2026-10-25), затем текст согласия. При изменении текста укажите новую версию"}
		if label, ok := labels[parts[1]]; ok {
			materials, err := h.Admin.Materials(ctx, r.ActorID)
			if err != nil {
				return err
			}
			current := map[string]string{"rules": materials.Rules, "instructions": materials.Instructions, "banner": materials.BannerRef, "consent": materials.ConsentVersion + "\n" + materials.ConsentText}[parts[1]]
			prompt := "Пришлите " + label + "."
			if strings.TrimSpace(current) != "" {
				prompt += "\n\nСейчас:\n" + current
			}
			return h.Begin(ctx, r, session.Flow{Action: parts[1]}, prompt)
		}
	}
	if action == "revoke" && len(parts) == 3 {
		if !access.Organizer {
			return promotionadmin.ErrForbidden
		}
		target, err := session.ParseID(parts[1])
		if err != nil {
			return err
		}
		partner, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || partner < 0 {
			return promotionadmin.ErrInvalid
		}
		return h.Confirm(ctx, r, session.Flow{Action: "revoke", Target: partner, Text: strconv.FormatInt(target, 10), Nonce: r.UpdateID})
	}
	if len(parts) == 2 {
		return h.targetCallback(ctx, r, access, action, parts[1])
	}
	return h.Reply(ctx, r, "Кнопка устарела. Откройте /admin.", session.Back())
}

func (h *Handler) confirmCallback(ctx context.Context, r session.Request, nonce string) error {
	dialog, err := h.Dialogs.Load(ctx, r.UserID)
	if err != nil {
		return err
	}
	var f session.Flow
	if dialog == nil || dialog.State != "admin:confirm" {
		return h.Reply(ctx, r, "Подтверждение устарело. Откройте /admin.", session.Back())
	}
	if err := json.Unmarshal(dialog.Payload, &f); err != nil {
		return err
	}
	if nonce != strconv.FormatInt(f.Nonce, 10) {
		return h.Reply(ctx, r, "Это подтверждение относится к другому изменению. Откройте /admin.", session.Back())
	}
	return (&forms.Handler{Session: h.Session}).Apply(ctx, r, f)
}

func (h *Handler) menuCallback(ctx context.Context, r session.Request, access promotionadminentity.Access, action string) error {
	switch action {
	case "home":
		return h.Home(ctx, r)
	case "partners":
		return h.partners(ctx, r, access)
	case "campaign":
		if !access.Organizer {
			return promotionadmin.ErrForbidden
		}
		current, err := h.Admin.Campaign(ctx, r.ActorID)
		if err != nil {
			return err
		}
		text := "Настройка акции: название, дата начала, дата окончания — по одному полю на строку. Формат даты: ГГГГ-ММ-ДД."
		if current != nil {
			text += "\n\nСейчас:\n" + current.Name + "\n" + current.StartsOn.Format("2006-01-02") + "\n" + current.EndsOn.Format("2006-01-02")
		}
		return h.Begin(ctx, r, session.Flow{Action: "campaign"}, text)
	case "materials":
		if !access.Organizer {
			return promotionadmin.ErrForbidden
		}
		return h.materialsMenu(ctx, r)
	case "policies":
		if !access.Organizer {
			return promotionadmin.ErrForbidden
		}
		return h.policiesMenu(ctx, r)
	case "final":
		if !access.Organizer {
			return promotionadmin.ErrForbidden
		}
		return h.drawForm(ctx, r, 0)
	case "final_reserve":
		count, err := h.Admin.FinalReserveCount(ctx, r.ActorID)
		if err != nil {
			return err
		}
		return h.Begin(ctx, r, session.Flow{Action: "final_reserve"}, fmt.Sprintf("Число резервистов финала. Сейчас: %d (по умолчанию 5). Пришлите целое число от 1 до 10000. Сначала настройте финальный розыгрыш; после проведения число менять нельзя.", count))
	case "grants":
		return h.grants(ctx, r)
	case "partner_new":
		if !access.Organizer {
			return promotionadmin.ErrForbidden
		}
		return h.Begin(ctx, r, session.Flow{Action: "partner"}, forms.PartnerPrompt)
	case "organizer_add":
		if !access.Organizer {
			return promotionadmin.ErrForbidden
		}
		return h.Begin(ctx, r, session.Flow{Action: "grant"}, "Введите @username или числовой Telegram ID нового организатора. Он сможет управлять всей акцией и доступами. Для добавления по нику человек должен сначала отправить боту /start.")
	}
	return h.Reply(ctx, r, "Кнопка устарела. Откройте /admin.", session.Back())
}

func (h *Handler) targetCallback(ctx context.Context, r session.Request, access promotionadminentity.Access, action, target string) error {
	id, err := session.ParseID(target)
	if err != nil {
		return err
	}
	switch action {
	case "partner":
		return h.partner(ctx, r, id)
	case "partner_edit":
		partner, err := h.findPartner(ctx, r, id)
		if err != nil {
			return err
		}
		return h.Begin(ctx, r, session.Flow{Action: "partner", Target: id}, forms.PartnerPrompt+"\n\nСейчас:\n"+partner.Name+"\n"+strconv.FormatInt(partner.TelegramChatID, 10)+"\n"+partner.SubscriptionURL)
	case "days":
		return h.days(ctx, r, id)
	case "day_new":
		if err := h.Admin.AuthorizePartner(ctx, r.ActorID, id); err != nil {
			return err
		}
		return h.Begin(ctx, r, session.Flow{Action: "day", Parent: id}, forms.DayPrompt)
	case "day":
		return h.day(ctx, r, id)
	case "day_edit":
		day, err := h.FindDay(ctx, r, id)
		if err != nil {
			return err
		}
		return h.Begin(ctx, r, session.Flow{Action: "day", Target: id, Parent: day.PartnerID}, forms.DayPrompt+"\n\nСейчас:\n"+forms.FormatDay(day))
	case "day_toggle":
		day, err := h.FindDay(ctx, r, id)
		if err != nil {
			return err
		}
		return h.Confirm(ctx, r, session.Flow{Action: "toggle", Target: id, Text: strconv.FormatBool(!day.IsEnabled), Nonce: r.UpdateID})
	case "draw":
		return h.drawForm(ctx, r, id)
	case "import":
		if err := h.Admin.AuthorizePartner(ctx, r.ActorID, id); err != nil {
			return err
		}
		return h.Begin(ctx, r, session.Flow{Action: "import", Target: id}, "Импорт промокодов: пришлите список, по одному коду на строку. Пробелы внутри кода запрещены, длина — до 128 символов. Повторы будут пропущены; ранее выданные коды не изменятся. Большой список можно отправить несколькими подтверждаемыми партиями.")
	case "grant":
		if !access.Organizer {
			return promotionadmin.ErrForbidden
		}
		if _, err := h.findPartner(ctx, r, id); err != nil {
			return err
		}
		return h.Begin(ctx, r, session.Flow{Action: "grant", Target: id}, "Введите @username или числовой Telegram ID администратора этого партнера. Для добавления по нику человек должен сначала отправить боту /start.")
	}
	return h.Reply(ctx, r, "Кнопка устарела. Откройте /admin.", session.Back())
}

func (h *Handler) partners(ctx context.Context, r session.Request, access promotionadminentity.Access) error {
	partners, err := h.Admin.Partners(ctx, r.ActorID)
	if err != nil {
		return err
	}
	var buttons []models.InlineKeyboardButton
	for _, p := range partners {
		buttons = append(buttons, session.Button(p.Name, fmt.Sprintf("partner:%d", p.ID)))
	}
	if access.Organizer {
		buttons = append(buttons, session.Button("Добавить партнера", "partner_new"))
	}
	buttons = append(buttons, session.Button("В админку", "home"))
	return h.Reply(ctx, r, "Выберите партнера:", session.Keyboard(buttons...))
}

func (h *Handler) findPartner(ctx context.Context, r session.Request, id int64) (promotionadminentity.PartnerConfig, error) {
	partners, err := h.Admin.Partners(ctx, r.ActorID)
	if err != nil {
		return promotionadminentity.PartnerConfig{}, err
	}
	for _, p := range partners {
		if p.ID == id {
			return p, nil
		}
	}
	return promotionadminentity.PartnerConfig{}, promotionadmin.ErrNotFound
}

func (h *Handler) partner(ctx context.Context, r session.Request, id int64) error {
	p, err := h.findPartner(ctx, r, id)
	if err != nil {
		return err
	}
	buttons := []models.InlineKeyboardButton{session.Button("Изменить партнера", fmt.Sprintf("partner_edit:%d", id)), session.Button("Расписание и призы", fmt.Sprintf("days:%d", id)), session.Button("Импорт промокодов", fmt.Sprintf("import:%d", id))}
	access, err := h.Access(ctx, r)
	if err != nil {
		return err
	}
	if access.Organizer {
		buttons = append(buttons, session.Button("Добавить администратора", fmt.Sprintf("grant:%d", id)))
	}
	buttons = append(buttons, session.Button("К партнерам", "partners"))
	return h.Reply(ctx, r, fmt.Sprintf("%s\nTelegram chat ID: %d\n%s", p.Name, p.TelegramChatID, p.SubscriptionURL), session.Keyboard(buttons...))
}

func (h *Handler) days(ctx context.Context, r session.Request, partnerID int64) error {
	if err := h.Admin.AuthorizePartner(ctx, r.ActorID, partnerID); err != nil {
		return err
	}
	days, err := h.Admin.Days(ctx, r.ActorID)
	if err != nil {
		return err
	}
	var buttons []models.InlineKeyboardButton
	for _, d := range days {
		if d.PartnerID == partnerID {
			label := d.Date.Format("02.01.2006")
			if !d.IsEnabled {
				label += " · прием отключен"
			}
			buttons = append(buttons, session.Button(label, fmt.Sprintf("day:%d", d.ID)))
		}
	}
	buttons = append(buttons, session.Button("Добавить день", fmt.Sprintf("day_new:%d", partnerID)), session.Button("К партнеру", fmt.Sprintf("partner:%d", partnerID)))
	return h.Reply(ctx, r, "Расписание партнера. Выберите день:", session.Keyboard(buttons...))
}

func (h *Handler) day(ctx context.Context, r session.Request, id int64) error {
	d, err := h.FindDay(ctx, r, id)
	if err != nil {
		return err
	}
	state, toggle := "Прием включен", "Отключить прием"
	if !d.IsEnabled {
		state, toggle = "Прием отключен", "Включить прием"
	}
	return h.Reply(ctx, r, state+"\n"+forms.FormatDay(d), session.Keyboard(session.Button("Изменить день", fmt.Sprintf("day_edit:%d", id)), session.Button(toggle, fmt.Sprintf("day_toggle:%d", id)), session.Button("Розыгрыш и призы", fmt.Sprintf("draw:%d", id)), session.Button("К расписанию", fmt.Sprintf("days:%d", d.PartnerID))))
}

func (h *Handler) materialsMenu(ctx context.Context, r session.Request) error {
	m, err := h.Admin.Materials(ctx, r.ActorID)
	if err != nil {
		return err
	}
	status := func(text string) string {
		if strings.TrimSpace(text) == "" {
			return "не задано"
		}
		return "задано"
	}
	text := fmt.Sprintf("Материалы акции\nПравила: %s\nИнструкция: %s\nБаннер: %s\nСогласие: %s\nВерсия согласия: %s\n\nТексты предоставляет организатор. Для изменения выберите материал.", status(m.Rules), status(m.Instructions), status(m.BannerRef), status(m.ConsentText), m.ConsentVersion)
	return h.Reply(ctx, r, text, session.Keyboard(session.Button("Правила", "material:rules"), session.Button("Инструкция", "material:instructions"), session.Button("Баннер", "material:banner"), session.Button("Согласие и версия", "material:consent"), session.Button("В админку", "home")))
}

func (h *Handler) policiesMenu(ctx context.Context, r session.Request) error {
	p, err := h.Admin.Policies(ctx, r.ActorID)
	if err != nil {
		return err
	}
	response := "не задан"
	if p.WinnerResponseHours > 0 {
		response = fmt.Sprintf("%d часов", p.WinnerResponseHours)
	}
	start := map[string]string{"": "не задан", "delivered": "доставка", "approved": "утверждение"}[p.ResponseStartsOn]
	failed := map[string]string{"": "не задано", "manual": "вручную", "reserve": "резерв"}[p.UndeliverablePolicy]
	text := fmt.Sprintf("Срок ответа: %s.\nОтсчет: %s.\nПри недоставке: %s.\n\nНастройте три строки:\n1. Срок ответа победителя в часах.\n2. Отсчет: доставка или утверждение.\n3. При недоставке: вручную или резерв.", response, start, failed)
	return h.Begin(ctx, r, session.Flow{Action: "policies"}, text)
}

func (h *Handler) grants(ctx context.Context, r session.Request) error {
	grants, err := h.Admin.Grants(ctx, r.ActorID)
	if err != nil {
		return err
	}
	text := "Доступы. Основные организаторы из конфигурации защищены от удаления через бота. Для удаления остальных нажмите на роль.\n"
	var buttons []models.InlineKeyboardButton
	for _, g := range grants {
		if g.FromConfig {
			text += fmt.Sprintf("\n%d — основной организатор, задан в конфигурации.", g.TelegramID)
			continue
		}
		role := "организатор"
		if g.PartnerID != 0 {
			role = fmt.Sprintf("партнер #%d", g.PartnerID)
		}
		buttons = append(buttons, session.Button(fmt.Sprintf("Отозвать: %d · %s", g.TelegramID, role), fmt.Sprintf("revoke:%d:%d", g.TelegramID, g.PartnerID)))
	}
	buttons = append(buttons, session.Button("Добавить организатора", "organizer_add"), session.Button("В админку", "home"))
	return h.Reply(ctx, r, text, session.Keyboard(buttons...))
}

func (h *Handler) drawForm(ctx context.Context, r session.Request, dayID int64) error {
	if dayID != 0 {
		if _, err := h.FindDay(ctx, r, dayID); err != nil {
			return err
		}
	}
	draws, err := h.Admin.Draws(ctx, r.ActorID)
	if err != nil {
		return err
	}
	f := session.Flow{Action: "draw", Parent: dayID}
	text := "Розыгрыш и призы. Первая строка — дата и время розыгрыша ГГГГ-ММ-ДД ЧЧ:ММ (МСК).\nЗатем по одному виду приза на строку:\nНазвание | число победителей | единиц на победителя | инструкция\n\nЗнак | разделяет поля. Инструкцию можно заменить на «-»."
	if dayID == 0 {
		text += "\nВ финале требуется один вид приза: 5 победителей по 2 билета."
	}
	for _, d := range draws {
		if d.PartnerDayID == dayID {
			f.Target = d.ID
			text += "\n\nСейчас:\n" + forms.FormatDraw(d)
			break
		}
	}
	return h.Begin(ctx, r, f, text)
}
