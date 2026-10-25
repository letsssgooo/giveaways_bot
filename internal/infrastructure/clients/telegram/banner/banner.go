package banner

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/api"
	telegramprotocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

const (
	apiMethodSendPhoto    = "sendPhoto"
	builtinPhotoPrefix    = "builtin:"
	welcomeBannerFilename = "welcome.png"
)

//go:embed assets/welcome.png
var welcomeBanner []byte

func Send(ctx context.Context, sdk *bot.Bot, payload json.RawMessage) error {
	var params struct {
		ChatID int64  `json:"chat_id"`
		Photo  string `json:"photo"`
	}
	if api.DecodePayload(payload, &params) != nil || params.ChatID == 0 ||
		(strings.HasPrefix(params.Photo, builtinPhotoPrefix) && params.Photo != telegramprotocol.WelcomeBannerRef) {
		return &api.Error{Method: apiMethodSendPhoto, Code: http.StatusBadRequest, Reason: "invalid saved photo"}
	}
	var photo models.InputFile = &models.InputFileString{Data: params.Photo}
	if params.Photo == "" || params.Photo == telegramprotocol.WelcomeBannerRef {
		photo = &models.InputFileUpload{Filename: welcomeBannerFilename, Data: bytes.NewReader(welcomeBanner)}
	}
	_, err := sdk.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: params.ChatID, Photo: photo})
	return api.Wrap(apiMethodSendPhoto, err)
}
