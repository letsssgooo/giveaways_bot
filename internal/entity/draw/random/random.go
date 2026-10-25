package random

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"

	drawentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
)

// Select выбирает участников с вероятностью, пропорциональной весу. После
// выбора удаляется весь вес пользователя, поэтому победители и резерв различны.
func Select(participants []drawentity.Participant, count int) ([]drawentity.Participant, error) {
	return selectWithReader(rand.Reader, participants, count)
}

func selectWithReader(random io.Reader, participants []drawentity.Participant, count int) ([]drawentity.Participant, error) {
	if count < 0 {
		return nil, errors.New("negative draw selection size")
	}
	remaining := slices.Clone(participants)
	var total int64
	for _, participant := range remaining {
		if participant.Weight < 1 || participant.Weight > 10 {
			return nil, errors.New("invalid draw participant weight")
		}
		total += int64(participant.Weight)
	}
	count = min(count, len(remaining))
	selected := make([]drawentity.Participant, 0, count)
	for range count {
		ticket, err := rand.Int(random, big.NewInt(total))
		if err != nil {
			return nil, fmt.Errorf("choose draw participant: %w", err)
		}
		position := ticket.Int64()
		for i, participant := range remaining {
			position -= int64(participant.Weight)
			if position < 0 {
				selected = append(selected, participant)
				total -= int64(participant.Weight)
				remaining = append(remaining[:i], remaining[i+1:]...)
				break
			}
		}
	}
	return selected, nil
}
