// Package sender содержит отправку сообщений через Telegram Bot API.
package sender

import (
	"context"
	"encoding/json"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
)

type Sender struct{ client *telegram.Client }

func New(client *telegram.Client) *Sender { return &Sender{client: client} }

func (s *Sender) Send(ctx context.Context, kind string, payload json.RawMessage) error {
	return s.client.Send(ctx, kind, payload)
}
