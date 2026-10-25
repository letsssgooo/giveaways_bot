// Package telegram содержит общие компоненты клиента Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/banner"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/commands"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/document"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/internal/api"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/subscription"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram/usernames"
	telegramprotocol "github.com/letsssgooo/giveaways_bot/internal/telegram"
)

const (
	apiBaseURL              = "https://api.telegram.org"
	maxResponseSize         = 8 << 20
	maxUpdateBatchSize      = 100
	maxHTTPErrorCode        = 599
	updateTypeMessage       = "message"
	updateTypeCallbackQuery = "callback_query"
	botDescription          = "Бот акции и розыгрышей. Нажмите /start, чтобы начать, или /help, чтобы получить справку."
)

const (
	apiMethodGetMe               = "getMe"
	apiMethodGetWebhookInfo      = "getWebhookInfo"
	apiMethodSetMyDescription    = "setMyDescription"
	apiMethodSetMyCommands       = "setMyCommands"
	apiMethodGetUpdates          = "getUpdates"
	apiMethodSendMessage         = "sendMessage"
	apiMethodAnswerCallbackQuery = "answerCallbackQuery"
)

// Client вызывает Bot API; запуск приема и подтверждение offset принадлежат приложению.
type Client struct {
	sdk         *bot.Bot
	http        *http.Client
	transport   *http.Transport
	updatesURL  string
	pollTimeout time.Duration
}

func New(token string, pollTimeout, requestTimeout time.Duration) (*Client, error) {
	return NewWithBaseURL(token, pollTimeout, requestTimeout, apiBaseURL)
}

// NewWithBaseURL позволяет подключить локальный Bot API или тестовый HTTP-сервер.
// В рабочей конфигурации приложение использует только New.
func NewWithBaseURL(token string, pollTimeout, requestTimeout time.Duration, baseURL string) (*Client, error) {
	if token == "" || strings.ContainsAny(token, "/?#%\\") || strings.ContainsFunc(token, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return nil, errors.New("telegram token is empty or contains invalid characters")
	}
	if pollTimeout <= 0 || pollTimeout%time.Second != 0 || requestTimeout <= pollTimeout {
		return nil, errors.New("telegram poll timeout must be positive whole seconds and smaller than request timeout")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Trim(parsed.Path, "/") != "" {
		return nil, errors.New("invalid telegram API base URL")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		// Bot API redirects are unexpected; never forward the token or payload.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	sdk, err := bot.New(token, bot.WithSkipGetMe(), bot.WithServerURL(strings.TrimRight(baseURL, "/")),
		bot.WithHTTPClient(pollTimeout, boundedClient{httpClient}), bot.WithErrorsHandler(func(error) {}))
	if err != nil {
		transport.CloseIdleConnections()
		return nil, errors.New("cannot initialize telegram client")
	}
	return &Client{
		sdk: sdk, http: httpClient, transport: transport, pollTimeout: pollTimeout,
		updatesURL: strings.TrimRight(baseURL, "/") + "/bot" + token + "/" + apiMethodGetUpdates,
	}, nil
}

func (c *Client) Close() { c.transport.CloseIdleConnections() }

func (c *Client) GetMe(ctx context.Context) (*models.User, error) {
	me, err := c.sdk.GetMe(ctx)
	if err != nil {
		return nil, api.Wrap(apiMethodGetMe, err)
	}
	if me == nil || me.ID <= 0 || !me.IsBot || me.Username == "" {
		return nil, &api.Error{Method: apiMethodGetMe, Reason: "invalid bot identity"}
	}
	return me, nil
}

// Configure проверяет режим получения событий и устанавливает базовые команды.
func (c *Client) Configure(ctx context.Context) error {
	webhook, err := c.sdk.GetWebhookInfo(ctx)
	if err != nil {
		return api.Wrap(apiMethodGetWebhookInfo, err)
	}
	if webhook == nil {
		return &api.Error{Method: apiMethodGetWebhookInfo, Reason: "invalid response"}
	}
	if webhook.URL != "" {
		return &api.Error{Method: apiMethodGetWebhookInfo, Code: http.StatusConflict, Reason: "active webhook must be removed before using long polling"}
	}
	ok, err := c.sdk.SetMyDescription(ctx, &bot.SetMyDescriptionParams{
		Description: botDescription,
	})
	if err != nil {
		return api.Wrap(apiMethodSetMyDescription, err)
	}
	if !ok {
		return &api.Error{Method: apiMethodSetMyDescription, Reason: "operation was not confirmed"}
	}
	ok, err = c.sdk.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: commands.List(false)})
	if err != nil {
		return api.Wrap(apiMethodSetMyCommands, err)
	}
	if !ok {
		return &api.Error{Method: apiMethodSetMyCommands, Reason: "operation was not confirmed"}
	}
	return nil
}

// Fetch получает пакет без изменения offset. Следующий offset передается только
// после сохранения всего пакета: именно следующий запрос подтверждает события Telegram.
func (c *Client) Fetch(ctx context.Context, offset int64) ([]models.Update, error) {
	if offset < 0 {
		return nil, &api.Error{Method: apiMethodGetUpdates, Code: http.StatusBadRequest, Reason: "negative offset is forbidden"}
	}
	payload, err := json.Marshal(struct {
		Offset         int64    `json:"offset"`
		Limit          int      `json:"limit"`
		Timeout        int64    `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{offset, maxUpdateBatchSize, int64(c.pollTimeout / time.Second), []string{updateTypeMessage, updateTypeCallbackQuery}})
	if err != nil {
		return nil, api.Wrap(apiMethodGetUpdates, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.updatesURL, bytes.NewReader(payload))
	if err != nil {
		return nil, api.Wrap(apiMethodGetUpdates, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (boundedClient{c.http}).Do(req)
	if err != nil {
		return nil, api.Wrap(apiMethodGetUpdates, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, api.Wrap(apiMethodGetUpdates, err)
	}
	var envelope response
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, api.Wrap(apiMethodGetUpdates, err)
	}
	if !envelope.OK {
		return nil, envelope.failure(apiMethodGetUpdates, 0)
	}
	var updates []models.Update
	if err := json.Unmarshal(envelope.Result, &updates); err != nil {
		return nil, api.Wrap(apiMethodGetUpdates, err)
	}
	for _, update := range updates {
		if update.ID <= 0 {
			return nil, &api.Error{Method: apiMethodGetUpdates, Reason: "update has no valid ID"}
		}
	}
	return updates, nil
}

// Send отправляет сохраненное сообщение. Повторы и retry_after обрабатывает
// исполнитель очереди, чтобы состояние попытки оставалось в PostgreSQL.
func (c *Client) Send(ctx context.Context, kind string, payload json.RawMessage) error {
	switch kind {
	case telegramprotocol.MessageKindText:
		var params struct {
			ChatID      int64                        `json:"chat_id"`
			Text        string                       `json:"text"`
			ReplyMarkup *models.InlineKeyboardMarkup `json:"reply_markup,omitempty"`
		}
		if api.DecodePayload(payload, &params) != nil || params.ChatID == 0 || params.Text == "" || api.TextLength(params.Text) > telegramprotocol.MaxMessageLength {
			return &api.Error{Method: apiMethodSendMessage, Code: http.StatusBadRequest, Reason: "invalid saved message"}
		}
		message := &bot.SendMessageParams{ChatID: params.ChatID, Text: params.Text}
		if params.ReplyMarkup != nil {
			message.ReplyMarkup = params.ReplyMarkup
		}
		_, err := c.sdk.SendMessage(ctx, message)
		return api.Wrap(apiMethodSendMessage, err)
	case telegramprotocol.MessageKindPhoto:
		return banner.Send(ctx, c.sdk, payload)
	case telegramprotocol.MessageKindCallbackAnswer:
		var params bot.AnswerCallbackQueryParams
		if api.DecodePayload(payload, &params) != nil || params.CallbackQueryID == "" {
			return &api.Error{Method: apiMethodAnswerCallbackQuery, Code: http.StatusBadRequest, Reason: "invalid saved callback answer"}
		}
		ok, err := c.sdk.AnswerCallbackQuery(ctx, &params)
		if err != nil {
			return api.Wrap(apiMethodAnswerCallbackQuery, err)
		}
		if !ok {
			return &api.Error{Method: apiMethodAnswerCallbackQuery, Reason: "operation was not confirmed"}
		}
		return nil
	default:
		return &api.Error{Method: "send", Code: http.StatusBadRequest, Reason: "unsupported saved message kind"}
	}
}

type response struct {
	OK         bool            `json:"ok"`
	Result     json.RawMessage `json:"result"`
	ErrorCode  int             `json:"error_code"`
	Parameters struct {
		RetryAfter int64 `json:"retry_after"`
	} `json:"parameters"`
}

func (r response) failure(method string, status int) error {
	if r.ErrorCode >= http.StatusBadRequest && r.ErrorCode <= maxHTTPErrorCode {
		status = r.ErrorCode
	}
	return &api.Error{Method: method, Code: status, RetryDelay: api.RetryDuration(r.Parameters.RetryAfter), Reason: "API request failed"}
}

// boundedClient ограничивает ответы SDK; их содержимое никогда не входит в ошибки.
type boundedClient struct{ client *http.Client }

func (c boundedClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &boundedBody{ReadCloser: resp.Body, remaining: maxResponseSize}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer func() { _ = resp.Body.Close() }()
		var envelope response
		_ = json.NewDecoder(resp.Body).Decode(&envelope)
		return nil, envelope.failure("request", resp.StatusCode)
	}
	return resp, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.ReadCloser.Read(extra[:])
		if n > 0 {
			return 0, errors.New("telegram response exceeds size limit")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// SyncCommands обновляет список команд личного чата.
func (c *Client) SyncCommands(ctx context.Context, chatID int64, admin bool) error {
	return commands.Sync(ctx, c.sdk, chatID, admin)
}

// SendDocument отправляет сформированную CSV-выгрузку.
func (c *Client) SendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string) error {
	return document.Send(ctx, c.sdk, chatID, filename, data, caption)
}

// CheckSubscription проверяет участие пользователя в канале.
func (c *Client) CheckSubscription(ctx context.Context, chatID, userID int64) (bool, error) {
	return subscription.Check(ctx, c.sdk, chatID, userID)
}

// CheckUsername проверяет актуальное имя пользователя.
func (c *Client) CheckUsername(ctx context.Context, telegramID int64, username string) (bool, error) {
	return usernames.Check(ctx, c.sdk, telegramID, username)
}

func InvalidReportError() error          { return document.InvalidReportError() }
func RetryAfter(err error) time.Duration { return api.RetryAfter(err) }
func IsRateLimited(err error) bool       { return api.IsRateLimited(err) }
func IsPermanent(err error) bool         { return api.IsPermanent(err) }
func IsFatal(err error) bool             { return api.IsFatal(err) }
