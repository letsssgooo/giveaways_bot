//go:build integration

package tests_test

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/letsssgooo/giveaways_bot/tests/utils"
)

// Exercise the deployed migration runner too: executing SQL alone cannot verify
// Flyway's history, checksum validation, or the no-op on the second migrate.
func TestMigrationsPreserveHistoryAndMatchFreshInstall(t *testing.T) {
	upgraded := utils.NewDBAt(t, -1)
	fresh := utils.NewDBAt(t, -1)
	run := migrationFlyway(t, upgraded)
	run(upgraded, "-target=1", "migrate")
	utils.Exec(t, upgraded, `
		INSERT INTO users(telegram_user_id,chat_id,started_at) VALUES (101,101,'2020-01-01T08:00:00Z');
		INSERT INTO promotions(name,starts_on,ends_on) VALUES ('Historical','2020-01-01','2020-01-02');
		INSERT INTO partners(name,telegram_chat_id,subscription_url) VALUES ('Partner',-101,'https://t.me/partner');
		INSERT INTO partner_days(partner_id,date,opens_at,closes_at) VALUES
			(1,'2020-01-01','2020-01-01T07:00:00Z','2020-01-01T19:00:00Z');
		INSERT INTO promo_codes(partner_id,code,user_id,partner_day_id,issued_at)
			VALUES (1,'KEEP-CODE',1,1,'2020-01-01T09:00:00Z');
		INSERT INTO promo_code_requests(user_id,partner_day_id,update_id,requested_at,outcome)
			VALUES (1,1,12345,'2020-01-01T09:00:00Z','issued');
		INSERT INTO registrations(user_id,partner_day_id,registered_at,subscription_checked_at)
			VALUES (1,1,'2020-01-01T10:00:00Z','2020-01-01T09:59:59Z');
		INSERT INTO active_days VALUES (1,'2020-01-01','2020-01-01T09:00:00Z');`)
	run(upgraded, "-target=2", "migrate")
	utils.Exec(t, upgraded, `
		INSERT INTO draws(kind,partner_day_id,scheduled_at) VALUES ('daily',1,'2020-01-01T19:00:00Z');
		INSERT INTO prizes(draw_id,name,places_count,units_per_winner) VALUES (1,'Historical prize',1,2);
		INSERT INTO draw_participants(draw_id,draw_kind,user_id,registration_id,partner_day_id,weight)
			VALUES (1,'daily',1,1,1,1);
		INSERT INTO draw_results(draw_id,draw_participant_id,role,position,prize_id,place_number)
			VALUES (1,1,'winner',1,1,1);
		UPDATE draws SET performed_at='2020-01-01T19:01:00Z',approved_at='2020-01-01T19:02:00Z',approved_by=1;
		INSERT INTO prize_awards(draw_id,draw_result_id,prize_id,place_number,assigned_at,
			confirmed_at,first_name,last_name,consent_given_at,consent_version)
			VALUES (1,1,1,1,'2020-01-01T19:02:00Z','2020-01-01T19:05:00Z',
				'Анна','Иванова','2020-01-01T19:04:00Z','historical-v1');`)
	before := migrationHistory(t, upgraded)
	run(upgraded, "migrate")
	run(upgraded, "validate")
	if after := migrationHistory(t, upgraded); !reflect.DeepEqual(before, after) {
		t.Fatalf("historical data changed:\nbefore=%v\nafter=%v", before, after)
	}
	var claimState string
	if err := upgraded.QueryRow(t.Context(), `SELECT claim_state FROM prize_awards WHERE id=1`).Scan(&claimState); err != nil || claimState != "confirmed" {
		t.Fatalf("confirmed legacy award state=%s: %v", claimState, err)
	}
	var backfilled bool
	if err := upgraded.QueryRow(t.Context(), `SELECT user_id=1 AND payload='{}'::jsonb
		AND received_at='2020-01-01T09:00:00Z' AND processed_at=received_at
		FROM telegram_updates WHERE update_id=12345`).Scan(&backfilled); err != nil || !backfilled {
		t.Fatalf("legacy promo request not backfilled as processed: %v, %v", backfilled, err)
	}
	var hours int
	var origin, policy string
	if err := upgraded.QueryRow(t.Context(), `SELECT winner_response_hours,response_starts_on,undeliverable_policy
		FROM administration_settings`).Scan(&hours, &origin, &policy); err != nil || hours != 48 || origin != "delivered" || policy != "manual" {
		t.Fatalf("upgrade defaults: %d %s %s: %v", hours, origin, policy, err)
	}
	run(fresh, "migrate")
	if a, b := migrationSchema(t, upgraded), migrationSchema(t, fresh); !reflect.DeepEqual(a, b) {
		t.Fatalf("fresh/upgraded schema mismatch:\nupgraded=%v\nfresh=%v", a, b)
	}
	var historyBefore, historyAfter string
	if err := upgraded.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(h) ORDER BY installed_rank)::text FROM flyway_schema_history h`).Scan(&historyBefore); err != nil {
		t.Fatal(err)
	}
	run(upgraded, "migrate")
	if err := upgraded.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(h) ORDER BY installed_rank)::text FROM flyway_schema_history h`).Scan(&historyAfter); err != nil {
		t.Fatal(err)
	}
	if historyBefore != historyAfter || !reflect.DeepEqual(before, migrationHistory(t, upgraded)) {
		t.Fatal("repeat migrate changed history or business data")
	}
}

func migrationHistory(t *testing.T, db *pgxpool.Pool) []string {
	t.Helper()
	queries := []string{
		`SELECT to_jsonb(x)::text FROM (SELECT id,telegram_user_id,chat_id,started_at FROM users ORDER BY id) x`,
		`SELECT to_jsonb(x)::text FROM (SELECT id,partner_id,code,user_id,partner_day_id,issued_at FROM promo_codes ORDER BY id) x`,
		`SELECT to_jsonb(x)::text FROM (SELECT * FROM promo_code_requests ORDER BY id) x`,
		`SELECT to_jsonb(x)::text FROM (SELECT * FROM registrations ORDER BY id) x`,
		`SELECT to_jsonb(x)::text FROM (SELECT * FROM active_days ORDER BY user_id,date) x`,
		`SELECT to_jsonb(x)::text FROM (SELECT id,kind,partner_day_id,scheduled_at,performed_at,approved_at,approved_by FROM draws ORDER BY id) x`,
		`SELECT to_jsonb(x)::text FROM (SELECT * FROM prizes ORDER BY id) x`,
		`SELECT to_jsonb(x)::text FROM (SELECT * FROM draw_participants ORDER BY id) x`,
		`SELECT to_jsonb(x)::text FROM (SELECT * FROM draw_results ORDER BY id) x`,
		`SELECT to_jsonb(x)::text FROM (SELECT id,draw_id,draw_result_id,prize_id,place_number,is_current,assigned_at,
			confirmed_at,first_name,last_name,consent_given_at,consent_version FROM prize_awards ORDER BY id) x`,
	}
	var result []string
	for _, query := range queries {
		rows, err := db.Query(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			result = append(result, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

// Compare definitions instead of OIDs, which necessarily differ across schemas.
func migrationSchema(t *testing.T, db *pgxpool.Pool) []string {
	t.Helper()
	query := `SELECT definition FROM (
		SELECT 'column:'||c.relname||':'||a.attnum||':'||a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull||':'||a.attidentity::text||':'||a.attgenerated::text||':'||coalesce(pg_get_expr(d.adbin,d.adrelid),'') definition
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid
		LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
		WHERE n.nspname=current_schema() AND c.relkind IN ('r','p') AND a.attnum>0 AND NOT a.attisdropped AND c.relname<>'flyway_schema_history'
		UNION ALL SELECT 'constraint:'||c.relname||':'||x.conname||':'||pg_get_constraintdef(x.oid)
		FROM pg_constraint x JOIN pg_class c ON c.oid=x.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=current_schema() AND c.relname<>'flyway_schema_history'
		UNION ALL SELECT 'index:'||indexname||':'||indexdef FROM pg_indexes WHERE schemaname=current_schema() AND tablename<>'flyway_schema_history'
		UNION ALL SELECT 'trigger:'||c.relname||':'||pg_get_triggerdef(x.oid) FROM pg_trigger x JOIN pg_class c ON c.oid=x.tgrelid
		JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT x.tgisinternal
		UNION ALL SELECT 'function:'||p.proname||':'||pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema()
		UNION ALL SELECT 'sequence:'||sequencename||':'||start_value||':'||min_value||':'||max_value||':'||increment_by||':'||cycle||':'||cache_size
		FROM pg_sequences WHERE schemaname=current_schema()
	) definitions ORDER BY definition`
	rows, err := db.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	schema := db.Config().ConnConfig.RuntimeParams["search_path"]
	var result []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		result = append(result, strings.ReplaceAll(s, schema, "test_schema"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func migrationFlyway(t *testing.T, db *pgxpool.Pool) func(*pgxpool.Pool, ...string) {
	t.Helper()
	name := "giveaways-flyway-" + strings.TrimPrefix(db.Config().ConnConfig.RuntimeParams["search_path"], "integration_")
	args := []string{"run", "--detach", "--rm", "--name", name, "--entrypoint", "/bin/sh"}
	if container := utils.PostgresContainer(); container != "" {
		args = append(args, "--network", "container:"+container)
	} else {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	args = append(args, "flyway/flyway:13.11.1", "-c", "sleep 1800")
	migrationDocker(t, args...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
			t.Errorf("remove Flyway: %v %s", err, output)
		}
	})
	migrations := utils.Migrations(t)
	migrationDocker(t, "cp", filepath.Dir(migrations[0].Path)+"/.", name+":/flyway/sql")
	return func(target *pgxpool.Pool, extra ...string) {
		t.Helper()
		cfg := target.Config().ConnConfig
		host, port := cfg.Host, cfg.Port
		if utils.PostgresContainer() != "" {
			host, port = "127.0.0.1", 5432
		} else if host == "localhost" || host == "127.0.0.1" {
			host = "host.docker.internal"
		}
		jdbc := "jdbc:postgresql://" + net.JoinHostPort(host, fmt.Sprint(port)) + "/" + url.PathEscape(cfg.Database)
		args := []string{"exec", "-e", "FLYWAY_URL=" + jdbc, "-e", "FLYWAY_USER=" + cfg.User, "-e", "FLYWAY_PASSWORD=" + cfg.Password, name, "/flyway/flyway",
			"-schemas=" + cfg.RuntimeParams["search_path"], "-connectRetries=10"}
		migrationDocker(t, append(args, extra...)...)
	}
}

func migrationDocker(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("test Flyway Docker command failed: %v\n%s", err, output)
	}
}
