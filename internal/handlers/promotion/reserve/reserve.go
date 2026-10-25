// Package reserve разбирает настройку количества резервистов финала.
package reserve

import (
	"strconv"
	"strings"

	session "github.com/letsssgooo/giveaways_bot/internal/handlers/promotion/session"
)

func ParseCount(text string) (int, error) {
	count, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || count < 1 || count > 10000 {
		return 0, session.FormError("нужно целое число резервистов от 1 до 10000")
	}
	return count, nil
}
