// Package forms разбирает и сохраняет формы настройки акции.
package forms

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	grants "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/grants"
	reserve "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/reserve"
	session "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/session"
	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	promoimport "github.com/letsssgooo/giveaways_bot/internal/services/promotion/importer"
)

type Handler struct{ *session.Session }

var moscow = time.FixedZone("Europe/Moscow", 3*60*60)

const PartnerPrompt = "Партнер: три строки — название, числовой ID канала/группы (обычно начинается с -100), ссылка https://t.me/…"

const DayPrompt = "День партнера: шесть полей, каждое с новой строки.\n1. Дата: ГГГГ-ММ-ДД.\n2. Начало приема: ЧЧ:ММ (МСК).\n3. Конец приема: ЧЧ:ММ; 24:00 — следующая полночь.\n4. Срок действия промокода: ГГГГ-ММ-ДД ЧЧ:ММ или «-».\n5. Прием включен: да или нет.\n6. Инструкция по промокоду (можно несколько строк; «-» — без инструкции)."

func date(value string) (time.Time, error) {
	parsed, err := time.ParseInLocation(time.DateOnly, value, moscow)
	if err != nil {
		return time.Time{}, session.FormError("дата должна быть в формате ГГГГ-ММ-ДД")
	}
	return parsed, nil
}

func timestamp(value string) (time.Time, error) {
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, moscow)
	if err != nil {
		return time.Time{}, session.FormError("дата и время должны быть в формате ГГГГ-ММ-ДД ЧЧ:ММ")
	}
	return parsed, nil
}

func parseCampaign(text string) (promotionadminentity.CampaignConfig, error) {
	var v promotionadminentity.CampaignConfig
	p := session.Lines(text)
	if len(p) != 3 {
		return v, session.FormError("нужны три строки: название, начало, окончание")
	}
	v.Name = p[0]
	var err error
	if v.StartsOn, err = date(p[1]); err != nil {
		return v, err
	}
	v.EndsOn, err = date(p[2])
	return v, err
}

func parsePartner(text string, id int64) (promotionadminentity.PartnerConfig, error) {
	v := promotionadminentity.PartnerConfig{ID: id}
	p := session.Lines(text)
	if len(p) != 3 {
		return v, session.FormError("нужны три строки: название, ID канала, ссылка")
	}
	v.Name, v.SubscriptionURL = p[0], p[2]
	id, err := strconv.ParseInt(p[1], 10, 64)
	if err != nil || id >= 0 {
		return v, session.FormError("нужен отрицательный числовой ID канала/группы")
	}
	v.TelegramChatID = id
	return v, nil
}

func parseDay(f session.Flow) (promotionadminentity.DayConfig, error) {
	v := promotionadminentity.DayConfig{ID: f.Target, PartnerID: f.Parent}
	p := session.Lines(f.Text)
	if len(p) < 6 {
		return v, session.FormError("нужны дата, начало, конец, срок промокода, да/нет и инструкция")
	}
	var err error
	if v.Date, err = date(p[0]); err != nil {
		return v, err
	}
	if v.OpensAt, err = timestamp(p[0] + " " + p[1]); err != nil {
		return v, err
	}
	if p[2] == "24:00" {
		v.ClosesAt = v.Date.AddDate(0, 0, 1)
	} else if v.ClosesAt, err = timestamp(p[0] + " " + p[2]); err != nil {
		return v, err
	}
	if p[3] != "-" {
		until, err := timestamp(p[3])
		if err != nil {
			return v, err
		}
		v.PromoValidUntil = &until
	}
	switch strings.ToLower(p[4]) {
	case "да":
		v.IsEnabled = true
	case "нет":
		v.IsEnabled = false
	default:
		return v, session.FormError("для включения приема укажите да или нет")
	}
	v.PromoInstructions = strings.Join(p[5:], "\n")
	if v.PromoInstructions == "-" {
		v.PromoInstructions = ""
	}
	return v, nil
}

func parseDraw(f session.Flow) (promotionadminentity.DrawConfig, error) {
	v := promotionadminentity.DrawConfig{ID: f.Target, PartnerDayID: f.Parent, Kind: "daily"}
	if f.Parent == 0 {
		v.Kind = "final"
	}
	p := session.Lines(f.Text)
	if len(p) < 2 {
		return v, session.FormError("нужны дата розыгрыша и хотя бы один приз")
	}
	var err error
	if v.ScheduledAt, err = timestamp(p[0]); err != nil {
		return v, err
	}
	for _, line := range p[1:] {
		fields := strings.Split(line, "|")
		if len(fields) != 4 {
			return v, session.FormError("приз: название | число победителей | единиц | инструкция")
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		places, e1 := strconv.Atoi(fields[1])
		units, e2 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || places <= 0 || units <= 0 {
			return v, session.FormError("число победителей и единиц должно быть положительным целым")
		}
		if fields[3] == "-" {
			fields[3] = ""
		}
		v.Prizes = append(v.Prizes, promotionadminentity.PrizeConfig{Name: fields[0], PlacesCount: places, UnitsPerWinner: units, Instructions: fields[3]})
	}
	return v, nil
}

func parsePolicies(text string) (promotionadminentity.Policies, error) {
	var v promotionadminentity.Policies
	p := session.Lines(text)
	if len(p) != 3 {
		return v, session.FormError("нужны три строки: часы, начало отсчета, действие при недоставке")
	}
	hours, err := strconv.Atoi(p[0])
	if err != nil || hours <= 0 {
		return v, session.FormError("срок ответа должен быть положительным целым числом")
	}
	v.WinnerResponseHours = hours
	switch strings.ToLower(p[1]) {
	case "доставка":
		v.ResponseStartsOn = "delivered"
	case "утверждение":
		v.ResponseStartsOn = "approved"
	default:
		return v, session.FormError("начало отсчета: доставка или утверждение")
	}
	switch strings.ToLower(p[2]) {
	case "вручную":
		v.UndeliverablePolicy = "manual"
	case "резерв":
		v.UndeliverablePolicy = "reserve"
	default:
		return v, session.FormError("при недоставке: вручную или резерв")
	}
	return v, nil
}

func Validate(f session.Flow) error {
	switch f.Action {
	case "final_reserve":
		_, err := reserve.ParseCount(f.Text)
		return err
	case "campaign":
		_, err := parseCampaign(f.Text)
		return err
	case "partner":
		_, err := parsePartner(f.Text, f.Target)
		return err
	case "day":
		_, err := parseDay(f)
		return err
	case "draw":
		_, err := parseDraw(f)
		return err
	case "policies":
		_, err := parsePolicies(f.Text)
		return err
	case "grant":
		if strings.HasPrefix(f.Text, "@") {
			_, err := grants.Username(f.Text)
			return err
		}
		_, err := session.ParseID(f.Text)
		return err
	case "revoke":
		_, err := session.ParseID(f.Text)
		return err
	case "consent":
		version, text, found := strings.Cut(f.Text, "\n")
		if !found || strings.TrimSpace(version) == "" || strings.TrimSpace(text) == "" {
			return session.FormError("первая строка — версия согласия, остальные — его текст")
		}
	}
	return nil
}

func FormatDay(d promotionadminentity.DayConfig) string {
	closes := d.ClosesAt.In(moscow).Format("15:04")
	if d.ClosesAt.In(moscow).Format(time.DateOnly) != d.Date.Format(time.DateOnly) {
		closes = "24:00"
	}
	expiry := "-"
	if d.PromoValidUntil != nil {
		expiry = d.PromoValidUntil.In(moscow).Format("2006-01-02 15:04")
	}
	enabled := "нет"
	if d.IsEnabled {
		enabled = "да"
	}
	instructions := d.PromoInstructions
	if instructions == "" {
		instructions = "-"
	}
	return strings.Join([]string{d.Date.Format(time.DateOnly), d.OpensAt.In(moscow).Format("15:04"), closes, expiry, enabled, instructions}, "\n")
}

func FormatDraw(d promotionadminentity.DrawConfig) string {
	text := d.ScheduledAt.In(moscow).Format("2006-01-02 15:04")
	for _, p := range d.Prizes {
		instructions := p.Instructions
		if instructions == "" {
			instructions = "-"
		}
		text += fmt.Sprintf("\n%s | %d | %d | %s", p.Name, p.PlacesCount, p.UnitsPerWinner, instructions)
	}
	return text
}

func (h *Handler) Apply(ctx context.Context, r session.Request, f session.Flow) error {
	var err error
	message := "Изменение сохранено."
	switch f.Action {
	case "final_reserve":
		var count int
		count, err = reserve.ParseCount(f.Text)
		if err == nil {
			err = h.Admin.SaveFinalReserveCount(ctx, r.ActorID, count)
		}
	case "campaign":
		var v promotionadminentity.CampaignConfig
		v, err = parseCampaign(f.Text)
		if err == nil {
			err = h.Admin.SaveCampaign(ctx, r.ActorID, v)
		}
	case "partner":
		var v promotionadminentity.PartnerConfig
		v, err = parsePartner(f.Text, f.Target)
		if err == nil {
			_, err = h.Admin.SavePartner(ctx, r.ActorID, v)
		}
	case "day":
		var v promotionadminentity.DayConfig
		v, err = parseDay(f)
		if err == nil {
			_, err = h.Admin.SaveDay(ctx, r.ActorID, v)
		}
	case "toggle":
		var v promotionadminentity.DayConfig
		v, err = h.FindDay(ctx, r, f.Target)
		if err == nil {
			v.IsEnabled = f.Text == "true"
			_, err = h.Admin.SaveDay(ctx, r.ActorID, v)
		}
	case "draw":
		var v promotionadminentity.DrawConfig
		v, err = parseDraw(f)
		if err == nil {
			_, err = h.Admin.SaveDraw(ctx, r.ActorID, v)
		}
	case "policies":
		var v promotionadminentity.Policies
		v, err = parsePolicies(f.Text)
		if err == nil {
			err = h.Admin.SavePolicies(ctx, r.ActorID, v)
		}
	case "rules", "instructions", "banner", "consent":
		err = h.applyMaterials(ctx, r, f)
	case "import":
		var result promoimport.ImportResult
		result, err = h.Importer.ImportCodes(ctx, r.ActorID, f.Target, f.Text)
		message = fmt.Sprintf("Импорт завершен. Добавлено: %d. Уже были загружены: %d. Повторы внутри списка: %d.", result.Added, result.Existing, result.Duplicates)
	case "grant", "revoke":
		err = h.applyAccess(ctx, r, f)
	default:
		return promotionadmin.ErrInvalid
	}
	if err != nil {
		return err
	}
	if err := h.Dialogs.Clear(ctx, r.UserID); err != nil {
		return err
	}
	return h.Reply(ctx, r, message, session.Back())
}

func (h *Handler) applyMaterials(ctx context.Context, r session.Request, f session.Flow) error {
	v, err := h.Admin.Materials(ctx, r.ActorID)
	if err != nil {
		return err
	}
	switch f.Action {
	case "rules":
		v.Rules = f.Text
	case "instructions":
		v.Instructions = f.Text
	case "banner":
		v.BannerRef = f.Text
		if v.BannerRef == "-" {
			v.BannerRef = ""
		}
	case "consent":
		v.ConsentVersion, v.ConsentText, _ = strings.Cut(f.Text, "\n")
	}
	return h.Admin.SaveMaterials(ctx, r.ActorID, v)
}

func (h *Handler) applyAccess(ctx context.Context, r session.Request, f session.Flow) error {
	var id int64
	var err error
	if f.Action == "grant" {
		id, err = grants.Recipient(r, f, h.GrantVerification)
	} else {
		id, err = session.ParseID(f.Text)
	}
	if err != nil {
		return err
	}
	if f.Action == "grant" {
		return h.Admin.Grant(ctx, r.ActorID, id, f.Target)
	}
	return h.Admin.Revoke(ctx, r.ActorID, id, f.Target)
}
