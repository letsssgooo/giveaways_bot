// Package testutil создает локальный Bot API для тестов клиента.
package testutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
)

const Token = "12345:do-not-log-this-token"

func ClientFor(t *testing.T, handler http.HandlerFunc) *telegram.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := telegram.NewWithBaseURL(Token, time.Second, 2*time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}
func Respond(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}); err != nil {
		t.Error(err)
	}
}
