package sync

import (
	"errors"
	"fmt"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
)

// Outcome is what the writer did (or, in a dry run, would do) with a charge.
type Outcome string

// Outcomes. Only Categorized, SplitInPlace and NeedsManualMatch are recorded
// in the store; Skipped and AlreadyProcessed are never recorded, so a skipped
// charge is retried on the next run. The writer only updates transactions the
// bank imported; it never creates one.
const (
	Categorized      Outcome = "categorized"
	SplitInPlace     Outcome = "split_in_place"
	NeedsManualMatch Outcome = "needs_manual_match"
	Skipped          Outcome = "skipped"
	AlreadyProcessed Outcome = "already_processed"
)

// Mode is the split-in-place policy for charges that span several categories.
type Mode string

// Split-in-place modes.
const (
	// ModeAuto tries split-in-place and switches it off for the rest of the
	// run after the first rejection (a 400 or a silently ignored split); later
	// multi-category charges are flagged for a manual split.
	ModeAuto Mode = "auto"
	// ModeAlways tries split-in-place on every charge and never switches it
	// off; a rejected charge is still flagged for a manual split.
	ModeAlways Mode = "always"
)

// ParseMode parses the split_in_place config value. An empty value is
// ModeAuto, the configured default.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case "":
		return ModeAuto, nil
	case ModeAuto, ModeAlways:
		return m, nil
	default:
		return "", fmt.Errorf("split_in_place %q must be one of auto, always", s)
	}
}

// ChargeJob is one charge to write, with its splits already built by
// splitter.BuildSplits.
type ChargeJob struct {
	Charge         order.Charge
	OrderDisplayID string
	// AccountID is the YNAB account of the card that paid; "" when the card
	// is not mapped to an account.
	AccountID string
	// Splits are negative milliunit amounts summing exactly to
	// -Charge.AmountCents*10.
	Splits []splitter.Split
	// Members are the charges a combined job stands for (see CombineJobs);
	// nil for a single charge.
	Members []order.Charge
}

// Result is the outcome of one charge.
type Result struct {
	Key     string
	Outcome Outcome
	// TxnID is the matched YNAB transaction that was (or, in a dry run, would
	// be) written. Empty when nothing was written.
	TxnID  string
	Splits []splitter.Split
	DryRun bool
	// Note explains a Skipped outcome, lists ambiguous candidates, or records
	// an assumption or a partial failure.
	Note string
}

// ErrInvalidJob is matched (via errors.Is) by every *InvalidJobError.
var ErrInvalidJob = errors.New("sync: invalid charge job")

// InvalidJobError rejects a job before any request is sent: a split that does
// not sum exactly to the charge must never leave the process.
type InvalidJobError struct {
	Key    string
	Reason string
}

func (e *InvalidJobError) Error() string {
	return fmt.Sprintf("sync: invalid charge job %q: %s", e.Key, e.Reason)
}

// Is lets errors.Is match ErrInvalidJob.
func (e *InvalidJobError) Is(target error) bool { return target == ErrInvalidJob }
