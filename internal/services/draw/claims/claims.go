package claims

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	awardsentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw/awards"
)

var ErrInvalidName = errors.New("name must contain between 1 and 100 characters without control characters")

type ClaimsRepository interface {
	View(context.Context, int64, int64) (*awardsentity.Award, error)
	List(context.Context, int64) ([]awardsentity.Award, error)
	AcceptConsent(context.Context, int64, int64) (*awardsentity.Award, error)
	Confirm(context.Context, int64, int64, string, string) (*awardsentity.Award, error)
}

type ClaimsService struct{ repository ClaimsRepository }

func NewClaims(repository ClaimsRepository) *ClaimsService {
	return &ClaimsService{repository: repository}
}

func (s *ClaimsService) View(ctx context.Context, awardID, userID int64) (*awardsentity.Award, error) {
	if awardID <= 0 || userID <= 0 {
		return nil, awardsentity.ErrAwardUnavailable
	}
	return s.repository.View(ctx, awardID, userID)
}

func (s *ClaimsService) List(ctx context.Context, userID int64) ([]awardsentity.Award, error) {
	if userID <= 0 {
		return nil, awardsentity.ErrAwardUnavailable
	}
	return s.repository.List(ctx, userID)
}

func (s *ClaimsService) AcceptConsent(ctx context.Context, awardID, userID int64) (*awardsentity.Award, error) {
	if awardID <= 0 || userID <= 0 {
		return nil, awardsentity.ErrAwardUnavailable
	}
	return s.repository.AcceptConsent(ctx, awardID, userID)
}

// NormalizeName не ограничивает алфавит имени: допустимы пробелы, дефисы и
// апострофы. Управляющие символы запрещены и до удаления пробелов по краям.
func NormalizeName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", ErrInvalidName
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return "", ErrInvalidName
		}
	}
	name = strings.TrimSpace(name)
	if size := utf8.RuneCountInString(name); size < 1 || size > 100 {
		return "", ErrInvalidName
	}
	return name, nil
}

func (s *ClaimsService) Confirm(ctx context.Context, awardID, userID int64, firstName, lastName string) (*awardsentity.Award, error) {
	if awardID <= 0 || userID <= 0 {
		return nil, awardsentity.ErrAwardUnavailable
	}
	first, err := NormalizeName(firstName)
	if err != nil {
		return nil, err
	}
	last, err := NormalizeName(lastName)
	if err != nil {
		return nil, err
	}
	return s.repository.Confirm(ctx, awardID, userID, first, last)
}
