package sync

import (
	"errors"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

// Status is the reported state of one Row: a writer Outcome, or Failed.
type Status string

// Row statuses. All but StatusFailed mirror the writer's Outcomes.
const (
	StatusCategorized      = Status(Categorized)
	StatusSplitInPlace     = Status(SplitInPlace)
	StatusNeedsManualMatch = Status(NeedsManualMatch)
	StatusStagedForImport  = Status(StagedForImport)
	// StatusSkipped is not a failure: no match, ambiguous match, refund,
	// gift card, an order without card charges, or a refused forced rerun.
	StatusSkipped          = Status(Skipped)
	StatusAlreadyProcessed = Status(AlreadyProcessed)
	// StatusFailed is a charge (or order) that could not be processed: a
	// Walmart error for the order, a categorizer failure, an unmapped category
	// or a YNAB write error. Any Failed row makes the exit code 2.
	StatusFailed Status = "failed"
)

// Summary is the result of one Run. It holds no formatting; the CLI prints it.
type Summary struct {
	DryRun bool
	// Rows has one row per card charge, per skipped ledger entry and per order
	// that could not be fetched, in processing order.
	Rows []Row
	// StoppedEarly is set when a YNAB rate limit or a Walmart block stopped
	// the run; Rows then hold everything that finished before it.
	StoppedEarly *StopReason
}

// Row is one line of the summary table.
type Row struct {
	OrderDisplayID string
	// AmountCents is the charge amount; for a skipped ledger entry it keeps
	// the ledger sign (negative for a refund).
	AmountCents int64
	Status      Status
	TxnID       string
	Note        string
	// Splits is the planned or written split, for the verbose listing; empty
	// when no split was built (for example an already processed charge).
	Splits []SplitView
	// Err is a short, secret-free description of why the row Failed.
	Err string
}

// SplitView is one split part as the user reads it.
type SplitView struct {
	Category   string
	CategoryID string
	// AmountCents is positive: the part of the charge in this category.
	AmountCents int64
}

// StopSource says what stopped a run early.
type StopSource string

// Stop sources.
const (
	StopYNABRateLimit  StopSource = "ynab_rate_limit"
	StopWalmartBlocked StopSource = "walmart_blocked"
)

// StopKind is the precise cause of an early stop, for choosing the advice
// shown to the user without parsing the Reason text.
type StopKind string

// Stop kinds.
const (
	StopKindOther               StopKind = "other"
	StopKindYNABRateLimit       StopKind = "ynab_rate_limit"
	StopKindWalmartBotChallenge StopKind = "walmart_bot_challenge"
	StopKindWalmartStaleSession StopKind = "walmart_stale_session"
	StopKindWalmartRateLimited  StopKind = "walmart_rate_limited"
)

// StopReason explains an early stop.
type StopReason struct {
	Source StopSource
	// Kind is the precise cause; StopKindOther (or empty) when unknown, such
	// as a Walmart block of an unrecognized kind.
	Kind StopKind
	// Reason is a short label: "YNAB rate limit", or "Walmart blocked: " plus
	// the block kind (bot challenge, stale session, rate limited).
	Reason string
	// Err is the secret-free error text.
	Err string
}

// Exit codes (PRD §6.1).
const (
	ExitOK           = 0
	ExitConfigAuth   = 1
	ExitSomeFailed   = 2
	ExitStoppedEarly = 3
)

// Totals counts rows by status.
func (s Summary) Totals() map[Status]int {
	out := make(map[Status]int)
	for _, r := range s.Rows {
		out[r.Status]++
	}
	return out
}

// ExitCode maps the summary to the PRD §6.1 exit code: 3 when the run stopped
// early (this takes priority), else 2 when any row Failed, else 0. Skipped
// rows are not failures.
func (s Summary) ExitCode() int {
	if s.StoppedEarly != nil {
		return ExitStoppedEarly
	}
	for _, r := range s.Rows {
		if r.Status == StatusFailed {
			return ExitSomeFailed
		}
	}
	return ExitOK
}

// ExitCodeForError classifies an error returned by Run: nil is 0; a YNAB rate
// limit or a Walmart block is 3; everything else (invalid config, YNAB
// 401/403, a cancelled run, anything unexpected) is 1.
func ExitCodeForError(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ynab.ErrRateLimited), errors.Is(err, order.ErrBlocked):
		return ExitStoppedEarly
	default:
		return ExitConfigAuth
	}
}
