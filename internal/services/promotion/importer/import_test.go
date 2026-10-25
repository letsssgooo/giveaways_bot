package importer

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	promotionadmin "github.com/letsssgooo/giveaways_bot/internal/services/promotion/admin"
)

type importAuthorizer struct {
	actor, partner int64
	err            error
}

func (a *importAuthorizer) AuthorizePartner(_ context.Context, actor, partner int64) error {
	a.actor, a.partner = actor, partner
	return a.err
}

type importRepository struct {
	partner int64
	codes   []string
	added   int
	err     error
	calls   int
}

func (r *importRepository) AddCodes(_ context.Context, partner int64, codes []string) (int, error) {
	r.calls++
	r.partner, r.codes = partner, slices.Clone(codes)
	return r.added, r.err
}

func TestImportCodesValidatesAndDeduplicatesBeforeWriting(t *testing.T) {
	repository := &importRepository{added: 2}
	authorizer := &importAuthorizer{}
	result, err := NewImporter(repository, authorizer).ImportCodes(t.Context(), 42, 7,
		"\ufeff SUMMER-01 \r\n\r\nSUMMER-01\r\nsummer-01\nБОНУС🎁\n")
	if err != nil || result != (ImportResult{Added: 2, Existing: 1, Duplicates: 1}) {
		t.Fatalf("import result: %+v, %v", result, err)
	}
	if authorizer.actor != 42 || authorizer.partner != 7 || repository.partner != 7 || repository.calls != 1 ||
		!slices.Equal(repository.codes, []string{"SUMMER-01", "summer-01", "БОНУС🎁"}) {
		t.Fatalf("wrong authorization or changed codes: authorizer=%+v repository=%+v", authorizer, repository)
	}
}

func TestImportCodesRejectsWholeInvalidBatch(t *testing.T) {
	for name, input := range map[string]string{
		"empty":        "\n  \r\n\ufeff",
		"space":        "VALID\nNOT VALID\nALSO-VALID",
		"control":      "VALID\nBAD\x00CODE",
		"unicode gap":  "VALID\nBAD\u00a0CODE",
		"long":         "VALID\n" + strings.Repeat("я", 129),
		"invalid utf8": "VALID\n\xff",
		"oversized":    strings.Repeat("SAME\n", maxImportCodes+1),
	} {
		t.Run(name, func(t *testing.T) {
			repository := &importRepository{}
			result, err := NewImporter(repository, &importAuthorizer{}).ImportCodes(t.Context(), 42, 7, input)
			if !errors.Is(err, promotionadmin.ErrInvalid) || result != (ImportResult{}) || repository.calls != 0 {
				t.Fatalf("invalid batch reached storage: result=%+v err=%v calls=%d", result, err, repository.calls)
			}
		})
	}
}

func TestImportCodesChecksAccessAndPropagatesStorageErrors(t *testing.T) {
	repository := &importRepository{}
	_, err := NewImporter(repository, &importAuthorizer{err: promotionadmin.ErrForbidden}).ImportCodes(t.Context(), 42, 7, "CODE")
	if !errors.Is(err, promotionadmin.ErrForbidden) || repository.calls != 0 {
		t.Fatalf("unauthorized import reached storage: err=%v calls=%d", err, repository.calls)
	}
	failure := errors.New("storage unavailable")
	repository.err = failure
	result, err := NewImporter(repository, &importAuthorizer{}).ImportCodes(t.Context(), 42, 7, "CODE")
	if !errors.Is(err, failure) || result != (ImportResult{}) {
		t.Fatalf("storage error lost: result=%+v err=%v", result, err)
	}
}
