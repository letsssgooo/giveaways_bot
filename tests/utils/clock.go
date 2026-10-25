//go:build integration

package utils

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FreezeClock controls explicit clock_timestamp() calls made by application SQL.
// It shadows the function only in this test's isolated schema. Defaults and
// migration functions already bound to pg_catalog.clock_timestamp stay real.
// Use the returned pool for subsequent queries, before preparing application SQL.
func FreezeClock(t testing.TB, db *pgxpool.Pool, now time.Time) *pgxpool.Pool {
	t.Helper()
	var schema string
	if err := db.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	Exec(t, db, `CREATE TABLE integration_clock (id boolean PRIMARY KEY DEFAULT true CHECK (id), now timestamptz NOT NULL)`)
	Exec(t, db, `INSERT INTO integration_clock(now) VALUES ($1)`, now)
	// SQL-standard function bodies bind the table to the isolated schema at
	// creation time, including when a caller changes its search_path.
	Exec(t, db, `CREATE FUNCTION clock_timestamp() RETURNS timestamptz LANGUAGE SQL VOLATILE
		RETURN (SELECT now FROM integration_clock WHERE id)`)
	cfg := db.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize() + ",pg_catalog"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var got time.Time
	if err := pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now) {
		t.Fatalf("test clock = %s, want %s", got, now)
	}
	return pool
}

func SetTime(t testing.TB, db *pgxpool.Pool, now time.Time) {
	t.Helper()
	Exec(t, db, `UPDATE integration_clock SET now = $1 WHERE id`, now)
}
