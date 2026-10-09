package sync

import (
	"context"
	"time"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/matcher"
	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
)

const (
	oneDay = 24 * time.Hour
	// maxFutureSkew tolerates a charge dated up to a day ahead of the clock
	// (time zones), but no further.
	maxFutureSkew = oneDay
)

// stage pre-stages an unmatched charge as a user-entered transaction that YNAB
// merges when the bank import arrives, or skips it (unrecorded, so it retries
// next run) when staging is not safe.
func (w *Writer) stage(ctx context.Context, job ChargeJob, res Result) (Result, error) {
	if reason := w.stageBlocker(job); reason != "" {
		res.Outcome = Skipped
		res.Note = "no matching transaction; not pre-staged: " + reason
		w.log.Info("charge skipped", "key", res.Key, "reason", res.Note)
		return res, nil
	}
	if w.cfg.DryRun {
		res.Outcome = StagedForImport
		return res, nil
	}
	created, err := w.api.CreateTransaction(ctx, stagePayload(job, w.cfg.Payee))
	if err != nil {
		return res, writeErr(res.Key, "pre-stage transaction", "", err)
	}
	res.Outcome, res.TxnID = StagedForImport, created.ID
	w.record(ctx, job, &res)
	return res, nil
}

// stageBlocker returns why the charge must not be pre-staged, or "".
func (w *Writer) stageBlocker(job ChargeJob) string {
	charged := job.Charge.Date
	age := w.cfg.Now().Sub(charged)
	switch {
	case job.AccountID == "":
		return "no account mapping for the card"
	case charged.IsZero():
		return "no usable date for the charge"
	case -age > maxFutureSkew:
		return "charge date is in the future"
	case age >= time.Duration(w.cfg.StageMaxAgeDays)*oneDay:
		return "charge is too old to pre-stage"
	case job.OrderCharges > 1:
		return "the order has several charges, which the bank may post as one amount; waiting for the bank transaction"
	case w.windowBeforeLoaded(charged):
		return "charge date window starts before the loaded transaction range; widen -days or re-run"
	case matcher.HasAmountInAccount(w.matcherCharge(job), w.txns, w.cfg.MatchOptions):
		return "a transaction with the same amount already exists in the account"
	}
	return ""
}

// windowBeforeLoaded reports whether the charge's match window starts before
// the first loaded day, so a same-amount transaction could exist unseen.
func (w *Writer) windowBeforeLoaded(charged time.Time) bool {
	if w.cfg.LoadedFrom.IsZero() {
		return false
	}
	windowStart := calendarDay(charged).AddDate(0, 0, -w.cfg.MatchOptions.DaysBefore)
	return windowStart.Before(calendarDay(w.cfg.LoadedFrom))
}

// calendarDay is the wall-clock date of t at midnight UTC.
func calendarDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func stagePayload(job ChargeJob, payee string) ynab.SaveTransaction {
	amount := -job.Charge.AmountCents * milliPerCent
	approved := false
	y, m, d := job.Charge.Date.Date()
	t := ynab.SaveTransaction{
		AccountID: job.AccountID,
		Date:      ynab.NewDate(y, m, d),
		Amount:    &amount,
		PayeeName: payee,
		Approved:  &approved,
	}
	base := ""
	if catID, ok := splitter.SingleCategory(job.Splits); ok {
		t.CategoryID = &catID
		base = job.Splits[0].Memo
	} else {
		t.SubTransactions = saveSubs(job.Splits)
	}
	marker := memo.AppendMarker(base, job.Charge.Key)
	t.Memo = &marker
	return t
}
