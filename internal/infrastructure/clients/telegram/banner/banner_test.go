package banner_test

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/testutil"
)

//go:embed assets/welcome.png
var welcomeBanner []byte

func TestSendPhoto(t *testing.T) {
	for _, photo := range []string{"", "builtin:welcome", "stored-photo-file-id", "https://example.com/banner.png"} {
		t.Run(photo, func(t *testing.T) {
			client := testutil.ClientFor(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/sendPhoto") {
					t.Errorf("unexpected method: %s", r.URL.Path)
				}
				if err := r.ParseMultipartForm(8 << 20); err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = r.MultipartForm.RemoveAll() }()
				if r.FormValue("chat_id") != "501" {
					t.Errorf("chat_id=%s", r.FormValue("chat_id"))
				}
				if photo == "" || photo == "builtin:welcome" {
					file, header, err := r.FormFile("photo")
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = file.Close() }()
					content, err := io.ReadAll(file)
					if err != nil || header.Filename != "welcome.png" || !bytes.Equal(content, welcomeBanner) {
						t.Errorf("invalid embedded banner upload: %v", err)
					}
				} else if r.FormValue("photo") != photo {
					t.Errorf("photo=%q, want %q", r.FormValue("photo"), photo)
				}
				testutil.Respond(t, w, map[string]any{"message_id": 1})
			})
			payload, err := json.Marshal(map[string]any{"chat_id": 501, "photo": photo})
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Send(t.Context(), "send_photo", payload); err != nil {
				t.Fatal(err)
			}
		})
	}
}
