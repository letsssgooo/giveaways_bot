// Package draw объединяет чтение, проведение и утверждение розыгрышей в одной транзакции.
package draw

import (
	"github.com/jackc/pgx/v5"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/approval"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/execution"
	"github.com/letsssgooo/giveaways_bot/internal/infrastructure/repository/draw/storage"
)

type Repository struct {
	*storage.Repository
	*execution.Executor
	*approval.Approver
}

func New(tx pgx.Tx) *Repository {
	return &Repository{Repository: storage.New(tx), Executor: execution.New(tx), Approver: approval.New(tx)}
}
