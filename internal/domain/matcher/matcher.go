// Package matcher finds the YNAB transaction that corresponds to a Walmart
// charge. It is pure: transactions and charges are domain-owned types that
// callers convert from adapter types.
//
// Dates are compared by calendar day. Each time.Time contributes its own
// wall-clock year/month/day (in whatever location it carries), which is then
// anchored to midnight UTC; the day distance between two such values is exact.
// This avoids a late-evening local time landing on the wrong day.
package matcher

import (
	"regexp"
	"time"

	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
)

// Default match window, in days around the charge date.
const (
	DefaultDaysBefore = 2
	DefaultDaysAfter  = 10
)

const milliPerCent = 10

const defaultPayeePattern = `(?i)wal[- ]?mart|wm supercenter|\bwmt\b`

var defaultPayeeRE = regexp.MustCompile(defaultPayeePattern)

// DefaultPayeeRE returns the Walmart payee pattern from the product spec.
func DefaultPayeeRE() *regexp.Regexp {
	return defaultPayeeRE
}

// Txn is the subset of a YNAB transaction the matcher needs.
type Txn struct {
	ID              string
	AccountID       string
	PayeeName       string
	ImportPayeeName string
	Memo            string
	AmountMilli     int64
	Date            time.Time
	Deleted         bool
	IsTransfer      bool
	IsSplit         bool
}

// Charge is one Walmart card charge to match. AccountID empty means no
// account restriction.
type Charge struct {
	Key         string
	AmountCents int64
	Date        time.Time
	AccountID   string
}

// Options tune matching. A nil PayeeRE means DefaultPayeeRE.
type Options struct {
	DaysBefore int
	DaysAfter  int
	PayeeRE    *regexp.Regexp
}

// DefaultOptions returns the default window and payee pattern.
func DefaultOptions() Options {
	return Options{DaysBefore: DefaultDaysBefore, DaysAfter: DefaultDaysAfter, PayeeRE: DefaultPayeeRE()}
}

// Status is the outcome of a match attempt. NoMatch is the zero value so an
// unset Result is never mistaken for a match.
type Status int

// Match outcomes.
const (
	NoMatch Status = iota
	Matched
	Ambiguous
)

func (s Status) String() string {
	switch s {
	case Matched:
		return "matched"
	case Ambiguous:
		return "ambiguous"
	default:
		return "no_match"
	}
}

// Result is the outcome of Match. Txn is set only when Status is Matched;
// Candidates is set for Matched and Ambiguous (the closest-date set).
type Result struct {
	Status     Status
	Txn        Txn
	Candidates []Txn
	Reason     string
}

// Claim records a transaction as taken so later charges in the run skip it.
// Match never mutates the claimed map; the caller calls Claim after a match.
func Claim(claimed map[string]bool, id string) {
	claimed[id] = true
}

// Match chooses the transaction for charge. A candidate must be non-deleted,
// not a transfer, not already split, carry no itemize marker, not be claimed,
// have amount exactly -AmountCents*10 milliunits, have a payee (or import
// payee) matching the payee pattern, fall within the date window (inclusive,
// by calendar day), and, when charge.AccountID is set, be in that account.
//
// Among candidates the closest date (absolute day distance) wins. If several
// tie, they are interchangeable when all share one account (lowest transaction
// ID, lexicographically); a tie across accounts is Ambiguous.
func Match(charge Charge, txns []Txn, claimed map[string]bool, opts Options) Result {
	opts = withDefaults(opts)
	var cands []Txn
	for _, t := range txns {
		if isCandidate(charge, t, claimed, opts) {
			cands = append(cands, t)
		}
	}
	if len(cands) == 0 {
		return Result{Status: NoMatch, Reason: "no eligible transaction with matching amount, payee, date window and account"}
	}
	best := closest(charge.Date, cands)
	if len(best) == 1 || sameAccount(best) {
		return Result{Status: Matched, Txn: lowestID(best), Candidates: best}
	}
	return Result{
		Status:     Ambiguous,
		Candidates: best,
		Reason:     "multiple transactions tie on closest date across different accounts",
	}
}

// HasAmountInAccount reports whether ANY non-deleted transaction in the
// charge's account has exactly the charge's amount within the date window,
// regardless of payee, split state, marker or claims. It backs the safety check
// before pre-staging a transaction. An empty charge.AccountID means no account
// restriction, as in Match.
func HasAmountInAccount(charge Charge, txns []Txn, opts Options) bool {
	opts = withDefaults(opts)
	for _, t := range txns {
		if t.Deleted || !accountOK(charge, t) {
			continue
		}
		if t.AmountMilli == -charge.AmountCents*milliPerCent && inWindow(charge.Date, t.Date, opts) {
			return true
		}
	}
	return false
}

func withDefaults(opts Options) Options {
	if opts.PayeeRE == nil {
		opts.PayeeRE = defaultPayeeRE
	}
	return opts
}

func isCandidate(charge Charge, t Txn, claimed map[string]bool, opts Options) bool {
	switch {
	case t.Deleted, t.IsTransfer, t.IsSplit:
		return false
	case memo.HasMarker(t.Memo), claimed[t.ID]:
		return false
	case t.AmountMilli != -charge.AmountCents*milliPerCent:
		return false
	case !opts.PayeeRE.MatchString(t.PayeeName) && !opts.PayeeRE.MatchString(t.ImportPayeeName):
		return false
	case !accountOK(charge, t):
		return false
	}
	return inWindow(charge.Date, t.Date, opts)
}

func accountOK(charge Charge, t Txn) bool {
	return charge.AccountID == "" || t.AccountID == charge.AccountID
}

func inWindow(anchor, d time.Time, opts Options) bool {
	diff := daysBetween(anchor, d) // positive when d is after anchor
	return diff >= -opts.DaysBefore && diff <= opts.DaysAfter
}

// calendarDay anchors the wall-clock date of t to midnight UTC.
func calendarDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// daysBetween returns the whole calendar days from a to b.
func daysBetween(a, b time.Time) int {
	return int(calendarDay(b).Sub(calendarDay(a)) / (24 * time.Hour))
}

func absDays(anchor, d time.Time) int {
	n := daysBetween(anchor, d)
	if n < 0 {
		return -n
	}
	return n
}

// closest returns the candidates tied for the smallest day distance.
func closest(anchor time.Time, cands []Txn) []Txn {
	bestDist := absDays(anchor, cands[0].Date)
	best := []Txn{cands[0]}
	for _, c := range cands[1:] {
		switch dist := absDays(anchor, c.Date); {
		case dist < bestDist:
			bestDist, best = dist, []Txn{c}
		case dist == bestDist:
			best = append(best, c)
		}
	}
	return best
}

func sameAccount(txns []Txn) bool {
	for _, t := range txns[1:] {
		if t.AccountID != txns[0].AccountID {
			return false
		}
	}
	return true
}

func lowestID(txns []Txn) Txn {
	low := txns[0]
	for _, t := range txns[1:] {
		if t.ID < low.ID {
			low = t
		}
	}
	return low
}
