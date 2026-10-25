package participation

import "time"

type CodeOutcome string

const (
	CodeIssued      CodeOutcome = "issued"
	CodeRepeated    CodeOutcome = "repeated"
	CodeExhausted   CodeOutcome = "exhausted"
	CodeClosed      CodeOutcome = "closed"
	CodeUnavailable CodeOutcome = "unavailable"
)

type CodeResult struct {
	Outcome      CodeOutcome
	PartnerDayID int64
	PartnerName  string
	Code         string
	Instructions string
	Date         time.Time
	ValidUntil   *time.Time
	Chances      int
}

// CodeOption позволяет выбрать открытый день или снова посмотреть свой код.
type CodeOption struct {
	PartnerDayID int64
	PartnerName  string
	Date         time.Time
	HasCode      bool
}
