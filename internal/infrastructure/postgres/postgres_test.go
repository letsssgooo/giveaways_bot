package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOpenRejectsInvalidURLWithoutExposingCredentials(t *testing.T) {
	const secret = "do-not-print-password"
	pool, err := Open(context.Background(), "postgres://user:"+secret+"@localhost:invalid/database")
	if err == nil || pool != nil {
		t.Fatal("invalid connection configuration was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("connection error contains credentials")
	}
}

func TestOpenHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool, err := Open(ctx, "postgres://test:test@127.0.0.1:1/test?sslmode=disable")
	if pool != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got pool=%v, error=%v", pool, err)
	}
}
