//go:build integration

// Package utils provides isolated PostgreSQL schemas for integration tests.
package utils

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresImage = "postgres:18.6"

var databaseURL string
var postgresContainer string

// Run uses TEST_DATABASE_URL when set, otherwise owns a disposable Docker server.
// DATABASE_URL and the application's .env are deliberately never read.
func Run(m *testing.M) int {
	databaseURL = os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		postgresContainer = "giveaways-tests-" + randomID()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "run", "--detach", "--rm", "--name", postgresContainer,
			"-e", "POSTGRES_USER=integration", "-e", "POSTGRES_PASSWORD=integration",
			"-e", "POSTGRES_DB=integration", "-p", "127.0.0.1::5432", postgresImage, "-c", "max_connections=200")
		if output, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "start integration PostgreSQL: %v\n%s\nUse Docker or set TEST_DATABASE_URL to a disposable test database.\n", err, output)
			return 1
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if output, err := exec.CommandContext(ctx, "docker", "rm", "-f", postgresContainer).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "remove integration PostgreSQL: %v %s\n", err, output)
			}
		}()
		output, err := exec.CommandContext(ctx, "docker", "port", postgresContainer, "5432/tcp").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "resolve integration PostgreSQL port:", err)
			return 1
		}
		address := strings.TrimSpace(string(output))
		databaseURL = "postgres://integration:integration@" + address + "/integration?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid TEST_DATABASE_URL")
		return 1
	}
	defer pool.Close()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for pool.Ping(ctx) != nil {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "integration PostgreSQL did not become ready")
			return 1
		case <-ticker.C:
		}
	}
	return m.Run()
}

func randomID() string { return fmt.Sprintf("%x", rand.Text()[:12]) }

// NewDB creates a schema, applies the actual project migrations, and cleans it up.
func NewDB(t testing.TB) *pgxpool.Pool { return NewDBAt(t, 0) }

// NewDBAt uses migrations through version (zero means latest, negative means none).
func NewDBAt(t testing.TB, version int) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "integration_" + randomID()
	quoted := pgx.Identifier{schema}.Sanitize()
	Exec(t, admin, "CREATE SCHEMA "+quoted)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		defer admin.Close()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "20000"
	cfg.MaxConns = 32
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if version >= 0 {
		Migrate(t, db, 0, version)
	}
	return db
}

// DBURL includes the isolated search_path for App.Run, which opens its own pool.
func DBURL(db *pgxpool.Pool) string {
	cfg := db.Config()
	raw := cfg.ConnString()
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		parsed, _ := url.Parse(raw)
		query := parsed.Query()
		for key, value := range cfg.ConnConfig.RuntimeParams {
			query.Set(key, value)
		}
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	for key, value := range cfg.ConnConfig.RuntimeParams {
		raw += " " + key + "='" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "'", "\\'") + "'"
	}
	return raw
}
func PostgresContainer() string { return postgresContainer }

type Execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func Exec(t testing.TB, db Execer, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(t.Context(), query, args...); err != nil {
		t.Fatalf("fixture SQL: %v", err)
	}
}

type Migration struct {
	Version int
	Path    string
}

func Migrations(t testing.TB) []Migration {
	t.Helper()
	_, source, _, _ := runtime.Caller(0)
	files, err := filepath.Glob(filepath.Join(filepath.Dir(source), "../../migrations/V*__*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("locate migrations: %v", err)
	}
	result := make([]Migration, 0, len(files))
	for _, path := range files {
		n, err := strconv.Atoi(strings.Split(strings.TrimPrefix(filepath.Base(path), "V"), "__")[0])
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, Migration{n, path})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	return result
}

// Migrate executes each SQL migration in a separate transaction, like Flyway.
// The dedicated migration test also runs Flyway itself to verify its history/no-op behavior.
func Migrate(t testing.TB, db *pgxpool.Pool, after, through int) {
	t.Helper()
	for _, migration := range Migrations(t) {
		if migration.Version <= after || (through > 0 && migration.Version > through) {
			continue
		}
		body, err := os.ReadFile(migration.Path)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), string(body)); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatalf("migration %s: %v", filepath.Base(migration.Path), err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
