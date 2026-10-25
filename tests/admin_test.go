//go:build integration

package tests_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	appdelivery "github.com/letsssgooo/giveaways_bot/internal/app/delivery"
	adminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
	adminhandler "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion"
	reporthandler "github.com/letsssgooo/giveaways_bot/internal/handlers/reporting"
	telegramclient "github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/postgres"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/dialogs"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/notifications"
	promotionrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/promotion"
	reportrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/reporting"
	usersrepo "github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/users"
	adminservice "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
	importservice "github.com/letsssgooo/giveaways_bot/internal/services/promotion/importer"
	reportservice "github.com/letsssgooo/giveaways_bot/internal/services/reporting"
	usersservice "github.com/letsssgooo/giveaways_bot/internal/services/users"
	protocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
	"github.com/letsssgooo/giveaways_bot/tests/utils"
)

const adminOrganizerID int64 = 900
const adminPartnerActorID int64 = 100

type administrationFixture struct {
	partner, otherPartner, day, otherDay int64
}

func seedAdministration(t *testing.T, db *pgxpool.Pool) administrationFixture {
	t.Helper()
	utils.Exec(t, db, `INSERT INTO promotions(name,starts_on,ends_on) VALUES
		('Integration',(clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date-1,
		(clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date+1)`)
	var f administrationFixture
	for index, target := range []*int64{&f.partner, &f.otherPartner} {
		if err := db.QueryRow(t.Context(), `INSERT INTO partners(name,telegram_chat_id,subscription_url)
			VALUES($1,$2,$3) RETURNING id`, fmt.Sprintf("Partner %d", index+1), -10001-int64(index), fmt.Sprintf("https://t.me/partner%d", index+1)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	for index, target := range []*int64{&f.day, &f.otherDay} {
		partner := []int64{f.partner, f.otherPartner}[index]
		if err := db.QueryRow(t.Context(), `INSERT INTO partner_days(partner_id,date,opens_at,closes_at)
			SELECT $1,today,today::timestamp AT TIME ZONE 'Europe/Moscow',
			(today+1)::timestamp AT TIME ZONE 'Europe/Moscow'
			FROM (SELECT (clock_timestamp() AT TIME ZONE 'Europe/Moscow')::date today) d RETURNING id`, partner).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	utils.Exec(t, db, `INSERT INTO users(telegram_user_id,chat_id)
		SELECT id,id FROM unnest(ARRAY[100,200,201,202,900]::bigint[]) id`)
	utils.Exec(t, db, `INSERT INTO administration_grants(telegram_user_id,partner_id) VALUES($1,$2)`, adminPartnerActorID, f.partner)
	return f
}

func adminTx(t *testing.T, db *pgxpool.Pool, fn func(pgx.Tx) error) {
	t.Helper()
	if err := postgres.InTx(t.Context(), db, fn); err != nil {
		t.Fatal(err)
	}
}

func adminScalar(t *testing.T, db *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()
	var value int64
	if err := db.QueryRow(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func adminService(tx pgx.Tx) *adminservice.AdminService {
	return adminservice.NewAdmin(promotionrepo.New(tx), []int64{adminOrganizerID})
}

func adminCallback(updateID, actor, chatID int64, chatType models.ChatType, data string) *models.Update {
	return &models.Update{ID: updateID, CallbackQuery: &models.CallbackQuery{
		ID: fmt.Sprintf("callback-%d", updateID), Data: data, From: models.User{ID: actor},
		Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{ID: 1, Date: 1, Chat: models.Chat{ID: chatID, Type: chatType}}},
	}}
}

func adminMessage(updateID, actor int64, text string) *models.Update {
	return &models.Update{ID: updateID, Message: &models.Message{Text: text,
		From: &models.User{ID: actor}, Chat: models.Chat{ID: actor, Type: models.ChatTypePrivate}}}
}

func adminHandle(t *testing.T, db *pgxpool.Pool, update *models.Update, reporting bool) {
	t.Helper()
	encoded, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	utils.Exec(t, db, `INSERT INTO telegram_updates(update_id,payload) VALUES($1,$2)`, update.ID, encoded)
	adminTx(t, db, func(tx pgx.Tx) error {
		users := usersservice.New(usersrepo.New(tx))
		admin := adminService(tx)
		var handled bool
		var err error
		if reporting {
			handled, err = reporthandler.New(reportservice.New(reportrepo.New(tx), admin), users, dialogs.New(tx), notifications.New(tx)).Handle(t.Context(), update)
		} else {
			handled, err = adminhandler.New(admin, importservice.NewImporter(promotionrepo.New(tx), admin), users, dialogs.New(tx), notifications.New(tx), "integrationbot").Handle(t.Context(), update)
		}
		if err == nil && !handled {
			return errors.New("expected administrative handler to handle update")
		}
		return err
	})
}

func TestAdminPartnerScopeAndReports(t *testing.T) {
	db := utils.NewDB(t)
	f := seedAdministration(t, db)
	utils.Exec(t, db, `INSERT INTO registrations(user_id,partner_day_id,registered_at,subscription_checked_at)
		SELECT id,CASE telegram_user_id WHEN 200 THEN $1::bigint ELSE $2::bigint END,statement_timestamp(),statement_timestamp()
		FROM users WHERE telegram_user_id IN (200,201)`, f.day, f.otherDay)
	adminTx(t, db, func(tx pgx.Tx) error {
		service := adminService(tx)
		partners, err := service.Partners(t.Context(), adminPartnerActorID)
		if err != nil {
			return err
		}
		if len(partners) != 1 || partners[0].ID != f.partner {
			t.Fatalf("partner scope leaked: %+v", partners)
		}
		_, err = service.SavePartner(t.Context(), adminPartnerActorID, adminentity.PartnerConfig{ID: f.otherPartner, Name: "Hijacked", TelegramChatID: -10002, SubscriptionURL: "https://t.me/hijacked"})
		if !errors.Is(err, adminservice.ErrForbidden) {
			t.Fatalf("cross-partner mutation: %v", err)
		}
		reports := reportservice.New(reportrepo.New(tx), service)
		rows, err := reports.Participants(t.Context(), adminPartnerActorID, 0)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].TelegramID != 200 || rows[0].PartnerID != f.partner {
			t.Fatalf("participants scope leaked: %+v", rows)
		}
		if _, err := reports.Export(t.Context(), adminPartnerActorID, f.otherPartner, "participants"); !errors.Is(err, adminservice.ErrForbidden) {
			t.Fatalf("cross-partner export: %v", err)
		}
		all, err := reports.Participants(t.Context(), adminOrganizerID, 0)
		if err == nil && len(all) != 2 {
			t.Fatalf("organizer sees %d participants, want 2", len(all))
		}
		return err
	})
}

func TestAdminRejectsForgedAndGroupCallbacks(t *testing.T) {
	db := utils.NewDB(t)
	f := seedAdministration(t, db)
	for index, tc := range []struct {
		name        string
		actor, chat int64
		kind        models.ChatType
	}{
		{"group", adminPartnerActorID, -100500, models.ChatTypeGroup},
		{"mismatched private chat", 200, adminPartnerActorID, models.ChatTypePrivate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adminHandle(t, db, adminCallback(int64(index*2+1), tc.actor, tc.chat, tc.kind, fmt.Sprintf("admin:partner_edit:%d", f.partner)), false)
			adminHandle(t, db, adminCallback(int64(index*2+2), tc.actor, tc.chat, tc.kind, fmt.Sprintf("report:export:participants:%d", f.partner)), true)
		})
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM dialogs`); got != 0 {
		t.Fatalf("forged callback opened %d dialogs", got)
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM outbox_messages`); got != 0 {
		t.Fatalf("forged callback queued %d responses", got)
	}
	// A valid private chat still cannot use a different partner's callback ID.
	adminHandle(t, db, adminCallback(10, adminPartnerActorID, adminPartnerActorID, models.ChatTypePrivate, fmt.Sprintf("admin:partner_edit:%d", f.otherPartner)), false)
	adminHandle(t, db, adminCallback(11, adminPartnerActorID, adminPartnerActorID, models.ChatTypePrivate, fmt.Sprintf("report:export:participants:%d", f.otherPartner)), true)
	if got := adminScalar(t, db, `SELECT count(*) FROM dialogs`); got != 0 {
		t.Fatalf("cross-partner callback opened %d dialogs", got)
	}
	if got := adminScalar(t, db, `SELECT count(*) FROM outbox_messages WHERE kind=$1`, protocol.MessageKindReport); got != 0 {
		t.Fatalf("cross-partner callback queued %d reports", got)
	}
}

func TestAdminRevocationBeforeFormConfirmation(t *testing.T) {
	db := utils.NewDB(t)
	f := seedAdministration(t, db)
	adminHandle(t, db, adminCallback(1, adminPartnerActorID, adminPartnerActorID, models.ChatTypePrivate, fmt.Sprintf("admin:partner_edit:%d", f.partner)), false)
	adminHandle(t, db, adminMessage(2, adminPartnerActorID, "Changed partner\n-10001\nhttps://t.me/changed"), false)
	if got := adminScalar(t, db, `SELECT count(*) FROM dialogs WHERE state='admin:confirm'`); got != 1 {
		t.Fatalf("confirmation draft count = %d", got)
	}
	adminTx(t, db, func(tx pgx.Tx) error {
		return adminService(tx).Revoke(t.Context(), adminOrganizerID, adminPartnerActorID, f.partner)
	})
	// Reconstructing the handler also verifies that the draft survives process restart.
	adminHandle(t, db, adminCallback(3, adminPartnerActorID, adminPartnerActorID, models.ChatTypePrivate, "admin:confirm:2"), false)
	var name string
	if err := db.QueryRow(t.Context(), `SELECT name FROM partners WHERE id=$1`, f.partner).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Partner 1" {
		t.Fatalf("revoked actor saved form: %q", name)
	}
}

func TestReportChecksAccessAgainAtDelivery(t *testing.T) {
	db := utils.NewDB(t)
	f := seedAdministration(t, db)
	adminHandle(t, db, adminCallback(1, adminPartnerActorID, adminPartnerActorID, models.ChatTypePrivate, fmt.Sprintf("report:export:participants:%d", f.partner)), true)
	var payload json.RawMessage
	if err := db.QueryRow(t.Context(), `SELECT payload FROM outbox_messages WHERE kind=$1`, protocol.MessageKindReport).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":100,"type":"private"}}}`))
	}))
	t.Cleanup(server.Close)
	client, err := telegramclient.NewWithBaseURL("integration:token", time.Second, 3*time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	sender := appdelivery.New(client, db, []int64{adminOrganizerID})
	if err := sender.Send(t.Context(), protocol.MessageKindReport, payload); err != nil {
		t.Fatalf("authorized report: %v", err)
	}
	if sends.Load() != 1 {
		t.Fatalf("authorized report HTTP requests = %d", sends.Load())
	}
	adminTx(t, db, func(tx pgx.Tx) error {
		return adminService(tx).Revoke(t.Context(), adminOrganizerID, adminPartnerActorID, f.partner)
	})
	err = sender.Send(t.Context(), protocol.MessageKindReport, payload)
	if err == nil || !telegramclient.IsPermanent(err) {
		t.Fatalf("revoked report delivery error = %v, want permanent denial", err)
	}
	if sends.Load() != 1 {
		t.Fatalf("revoked report reached Telegram: requests=%d", sends.Load())
	}
}

func TestWinnerExportNamesRequireArchivedConsent(t *testing.T) {
	db := utils.NewDB(t)
	f := seedAdministration(t, db)
	utils.Exec(t, db, `INSERT INTO consent_versions(version,consent_text) VALUES('v1','Original consent'),('v2','Changed consent')`)
	// Current materials can change; consent on an award must match its archived version.
	utils.Exec(t, db, `UPDATE promotions SET consent_version='v2',consent_text='Changed consent'`)
	utils.Exec(t, db, `INSERT INTO registrations(user_id,partner_day_id,registered_at,subscription_checked_at)
		SELECT id,$1,statement_timestamp(),statement_timestamp() FROM users WHERE telegram_user_id BETWEEN 200 AND 202`, f.day)
	utils.Exec(t, db, `INSERT INTO draws(kind,partner_day_id,scheduled_at) VALUES('daily',$1,clock_timestamp()-interval '1 hour')`, f.day)
	utils.Exec(t, db, `INSERT INTO prizes(draw_id,name,places_count,units_per_winner) SELECT id,'Prize',3,1 FROM draws`)
	utils.Exec(t, db, `INSERT INTO draw_participants(draw_id,draw_kind,user_id,registration_id,partner_day_id,weight)
		SELECT d.id,'daily',r.user_id,r.id,r.partner_day_id,1 FROM draws d CROSS JOIN registrations r`)
	utils.Exec(t, db, `INSERT INTO draw_results(draw_id,draw_participant_id,role,position,prize_id,place_number)
		SELECT p.draw_id,p.id,'winner',row_number() OVER(ORDER BY p.user_id),prize.id,
		row_number() OVER(ORDER BY p.user_id) FROM draw_participants p JOIN prizes prize ON prize.draw_id=p.draw_id`)
	utils.Exec(t, db, `INSERT INTO prize_awards(draw_id,draw_result_id,prize_id,place_number,first_name,last_name,
		response_hours,response_starts_on,undeliverable_policy,claim_consent_version,claim_consent_text,
		consent_given_at,consent_version)
		SELECT draw_id,id,prize_id,place_number,'Name '||position,'Family '||position,
		48,'delivered','manual','v1','Original consent',
		CASE WHEN position=2 THEN NULL ELSE clock_timestamp() END,
		CASE WHEN position=3 THEN 'v2' ELSE 'v1' END FROM draw_results`)
	utils.Exec(t, db, `UPDATE draws SET performed_at=clock_timestamp()`)
	adminTx(t, db, func(tx pgx.Tx) error {
		service := reportservice.New(reportrepo.New(tx), adminService(tx))
		rows, err := service.Winners(t.Context(), adminPartnerActorID, f.partner)
		if err != nil {
			return err
		}
		if len(rows) != 3 {
			t.Fatalf("winner rows = %d", len(rows))
		}
		for i, row := range rows {
			if i == 0 {
				if row.FirstName != "Name 1" || row.LastName != "Family 1" || row.ConsentVersion != "v1" || row.ConsentGivenAt == nil {
					t.Fatalf("archived valid consent not exported: %+v", row)
				}
			} else if row.FirstName != "" || row.LastName != "" || row.ConsentVersion != "" || row.ConsentGivenAt != nil {
				t.Fatalf("unconfirmed/mismatched consent leaked personal data: %+v", row)
			}
		}
		document, err := service.Export(t.Context(), adminPartnerActorID, f.partner, "winners")
		if err == nil && (strings.Contains(string(document.Data), "Name 2") || strings.Contains(string(document.Data), "Family 3")) {
			t.Fatal("CSV leaked names without valid consent")
		}
		return err
	})
}
