// Package fetcher содержит получение обновлений Telegram Bot API.
package fetcher

import (
	"context"

	"github.com/go-telegram/bot/models"

	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/clients/telegram"
)

type Fetcher struct{ client *telegram.Client }

func New(client *telegram.Client) *Fetcher { return &Fetcher{client: client} }

func (f *Fetcher) Fetch(ctx context.Context, offset int64) ([]models.Update, error) {
	return f.client.Fetch(ctx, offset)
}
