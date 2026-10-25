package importer

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	promotionadminentity "github.com/letsssgooo/giveaways_bot/internal/entity/promotion/admin"
)

const maxImportCodes = 1000

type ImportRepository interface {
	AddCodes(ctx context.Context, partnerID int64, codes []string) (int, error)
}

type PartnerAuthorizer interface {
	AuthorizePartner(ctx context.Context, actorTelegramID, partnerID int64) error
}

type ImportResult struct {
	Added      int
	Existing   int
	Duplicates int
}

type ImportService struct {
	repository ImportRepository
	admins     PartnerAuthorizer
}

// NewImporter получает репозиторий и проверку прав в одной транзакции обработки update.
func NewImporter(repository ImportRepository, admins PartnerAuthorizer) *ImportService {
	return &ImportService{repository: repository, admins: admins}
}

// ImportCodes проверяет весь список до записи и сохраняет только новые коды.
// Существующие, в том числе выданные пользователям, остаются без изменений.
func (s *ImportService) ImportCodes(ctx context.Context, actorTelegramID, partnerID int64, text string) (ImportResult, error) {
	if err := s.admins.AuthorizePartner(ctx, actorTelegramID, partnerID); err != nil {
		return ImportResult{}, err
	}
	codes, duplicates, err := parseImportCodes(text)
	if err != nil {
		return ImportResult{}, err
	}
	added, err := s.repository.AddCodes(ctx, partnerID, codes)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import promo codes: %w", err)
	}
	return ImportResult{Added: added, Existing: len(codes) - added, Duplicates: duplicates}, nil
}

func parseImportCodes(text string) ([]string, int, error) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "\ufeff")
	codes := make([]string, 0)
	seen := make(map[string]struct{})
	duplicates, total := 0, 0
	for lineNumber, line := range strings.Split(text, "\n") {
		code := strings.TrimSpace(line)
		if code == "" {
			continue
		}
		total++
		if total > maxImportCodes {
			return nil, 0, fmt.Errorf("%w: не более %d промокодов за одну загрузку", promotionadminentity.ErrInvalid, maxImportCodes)
		}
		if !utf8.ValidString(code) || utf8.RuneCountInString(code) > 128 ||
			strings.ContainsFunc(code, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return nil, 0, fmt.Errorf("%w: строка %d — код должен содержать от 1 до 128 символов без пробелов и управляющих символов", promotionadminentity.ErrInvalid, lineNumber+1)
		}
		if _, exists := seen[code]; exists {
			duplicates++
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return nil, 0, fmt.Errorf("%w: список промокодов пуст", promotionadminentity.ErrInvalid)
	}
	return codes, duplicates, nil
}
