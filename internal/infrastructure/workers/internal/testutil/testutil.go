package testutil

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
)

func QuietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func APIFailure(t *testing.T, status int, retryAfter int) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"ok":false,"error_code":%d,"parameters":{"retry_after":%d}}`, status, retryAfter)
	}))
	t.Cleanup(server.Close)
	client, err := telegram.NewWithBaseURL("12345:test-token", time.Second, 2*time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	_, err = client.GetMe(t.Context())
	if err == nil {
		t.Fatal("expected fake API error")
	}
	return err
}
