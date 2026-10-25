package document_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/entity/reporting"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/testutil"
)

func TestSendDocumentUploadsCSVFromMemory(t *testing.T) {
	data := []byte("\xef\xbb\xbfTelegram ID;Имя\n123;\"Имя; с разделителем\"\n")
	client := testutil.ClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/sendDocument") {
			t.Error("incorrect document API request")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		if r.FormValue("chat_id") != "501" || r.FormValue("caption") != "Участники акции" || r.FormValue("parse_mode") != "" {
			t.Error("wrong document chat or plain-text caption")
		}
		file, header, err := r.FormFile("document")
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = file.Close() }()
		uploaded, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(uploaded, data) || header.Filename != "participants_7.csv" {
			t.Error("document filename or bytes changed during upload")
		}
		testutil.Respond(t, w, models.Message{ID: 10})
	})
	if err := client.SendDocument(t.Context(), 501, "participants_7.csv", data, "Участники акции"); err != nil {
		t.Fatal(err)
	}
}

func TestSendDocumentRejectsUnsafeOrOversizedRequests(t *testing.T) {
	client := testutil.ClientFor(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid document reached Telegram") })
	for _, filename := range []string{"", ".csv", "../report.csv", "dir/report.csv", "dir\\report.csv", "report.txt", "report.csv\r\nX: value", "secret\".csv"} {
		if err := client.SendDocument(t.Context(), 501, filename, []byte("data"), ""); !telegram.IsPermanent(err) {
			t.Errorf("filename %q: expected permanent rejection, got %v", filename, err)
		}
	}
	for _, tc := range []struct {
		chat    int64
		data    []byte
		caption string
	}{
		{chat: 0, data: []byte("data")},
		{chat: 501},
		{chat: 501, data: make([]byte, reporting.MaxExportBytes+1)},
		{chat: 501, data: []byte("data"), caption: strings.Repeat("🎁", 513)},
	} {
		if err := client.SendDocument(t.Context(), tc.chat, "report.csv", tc.data, tc.caption); !telegram.IsPermanent(err) {
			t.Errorf("invalid report was not rejected permanently: %v", err)
		}
	}
	if err := telegram.InvalidReportError(); !telegram.IsPermanent(err) || telegram.IsFatal(err) {
		t.Fatalf("forbidden report classification: %v", err)
	}
}

func TestSendDocumentPreservesErrorClassificationWithoutSensitiveData(t *testing.T) {
	for _, code := range []int{400, 403, 429, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			client := testutil.ClientFor(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				if err := json.NewEncoder(w).Encode(map[string]any{
					"ok": false, "error_code": code, "description": testutil.Token + " secret-user-data",
					"parameters": map[string]int{"retry_after": 7},
				}); err != nil {
					t.Error(err)
				}
			})
			err := client.SendDocument(t.Context(), 501, "private_report.csv", []byte("secret-user-data"), "private caption")
			if err == nil || strings.Contains(err.Error(), testutil.Token) || strings.Contains(err.Error(), "secret-user-data") || strings.Contains(err.Error(), "private_report") || strings.Contains(err.Error(), "private caption") {
				t.Fatalf("unsafe document error: %v", err)
			}
			if telegram.IsPermanent(err) != (code == 400 || code == 403) || telegram.IsFatal(err) {
				t.Fatalf("wrong document error classification: %v", err)
			}
			if code == 429 && telegram.RetryAfter(err) != 7*time.Second {
				t.Fatalf("document retry_after lost: %v", err)
			}
		})
	}
}
