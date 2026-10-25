package document

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/entity/reporting"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/api"
)

const (
	apiMethodSendDocument = "sendDocument"
	maxDocumentCaption    = 1024
	maxDocumentFilename   = 128
)

// InvalidReportError сообщает очереди о постоянной ошибке выгрузки, не раскрывая
// данные отчета или причину отказа в доступе.
func InvalidReportError() error {
	return &api.Error{Method: apiMethodSendDocument, Code: http.StatusForbidden, Reason: "report unavailable"}
}

// Send загружает CSV из памяти. Повторы и ограничения частоты остаются
// в обработчике outbox; содержимое выгрузки не попадает в его payload.
func Send(ctx context.Context, sdk *bot.Bot, chatID int64, filename string, data []byte, caption string) error {
	if chatID == 0 || !validDocumentFilename(filename) || len(data) == 0 || len(data) > reporting.MaxExportBytes || api.TextLength(caption) > maxDocumentCaption {
		return &api.Error{Method: apiMethodSendDocument, Code: http.StatusBadRequest, Reason: "invalid report document"}
	}
	_, err := sdk.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID: chatID, Caption: caption,
		Document:                    &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)},
		DisableContentTypeDetection: true,
	})
	return api.Wrap(apiMethodSendDocument, err)
}

func validDocumentFilename(filename string) bool {
	if len(filename) <= len(".csv") || len(filename) > maxDocumentFilename || filename[0] == '.' || !strings.HasSuffix(filename, ".csv") {
		return false
	}
	for _, char := range filename {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}
