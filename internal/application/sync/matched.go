package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
)

// processMatched writes a charge onto the YNAB transaction it matched.
func (w *Writer) processMatched(ctx context.Context, job ChargeJob, orig ynab.Transaction, res Result) (Result, error) {
	if catID, ok := splitter.SingleCategory(job.Splits); ok {
		return w.categorize(ctx, job, orig, catID, res)
	}
	if !w.splitInPlaceAllowed() {
		return w.sibling(ctx, job, orig, res)
	}
	return w.splitInPlace(ctx, job, orig, res)
}

// categorize sets the category and appends the marker to the memo.
func (w *Writer) categorize(ctx context.Context, job ChargeJob, orig ynab.Transaction, catID string, res Result) (Result, error) {
	if w.cfg.DryRun {
		res.Outcome, res.TxnID = Categorized, orig.ID
		return res, nil
	}
	newMemo := parentMemo(orig.Memo, job)
	_, err := w.api.UpdateTransaction(ctx, orig.ID, ynab.SaveTransaction{CategoryID: &catID, Memo: &newMemo})
	if err != nil {
		return res, writeErr(res.Key, "categorize", orig.ID, err)
	}
	res.Outcome, res.TxnID = Categorized, orig.ID
	w.record(ctx, job, &res)
	return res, nil
}

func (w *Writer) splitInPlaceAllowed() bool {
	switch w.cfg.SplitInPlace {
	case ModeNever:
		return false
	case ModeAuto:
		return !w.splitOff
	default:
		return true
	}
}

// splitFailed applies the split_in_place mode after a rejection: auto
// switches split-in-place off for the rest of the run, always never does.
func (w *Writer) splitFailed() {
	if w.cfg.SplitInPlace == ModeAuto && !w.splitOff {
		w.splitOff = true
		w.log.Info("split-in-place switched off for the rest of the run")
	}
}

// splitInPlace turns the matched transaction into a split and verifies that
// YNAB saved it. A 400 or a silently ignored split falls back to a sibling.
func (w *Writer) splitInPlace(ctx context.Context, job ChargeJob, orig ynab.Transaction, res Result) (Result, error) {
	if w.cfg.DryRun {
		res.Outcome = SplitInPlace
		res.TxnID = orig.ID
		res.Note = "assumes split-in-place accepted; a rejection would create a flagged sibling split instead"
		return res, nil
	}
	newMemo := parentMemo(orig.Memo, job)
	saved, err := w.api.UpdateTransaction(ctx, orig.ID, ynab.SaveTransaction{
		Memo:            &newMemo,
		SubTransactions: saveSubs(job.Splits),
	})
	switch {
	case err == nil && splitSaved(saved, job):
		res.Outcome = SplitInPlace
		res.TxnID = orig.ID
		w.record(ctx, job, &res)
		return res, nil
	case err == nil:
		// 200 but the split was not saved: the marker memo was, so undo it
		// before anything else.
		w.log.Warn("split-in-place silently ignored by YNAB; restoring memo", "key", res.Key, "txn_id", orig.ID)
		w.splitFailed()
		if err := w.restore(ctx, orig, saved); err != nil {
			return res, fmt.Errorf("charge %s: split-in-place was ignored and restoring transaction %s failed; "+
				"its memo now carries the marker %s, which hides it from matching - fix it by hand: %w",
				res.Key, orig.ID, memo.Marker(res.Key), err)
		}
	case isRejected(err):
		w.log.Info("split-in-place rejected by YNAB", "key", res.Key, "txn_id", orig.ID, "error", err)
		w.splitFailed()
	default:
		return res, writeErr(res.Key, "split in place", orig.ID, err)
	}
	return w.sibling(ctx, job, orig, res)
}

// restore puts back the original memo (and the category, if the ignored
// update changed it).
func (w *Writer) restore(ctx context.Context, orig, saved ynab.Transaction) error {
	original := orig.Memo
	payload := ynab.SaveTransaction{Memo: &original}
	if !sameCategory(orig.CategoryID, saved.CategoryID) {
		payload.CategoryID = orig.CategoryID
		payload.ClearCategory = orig.CategoryID == nil
	}
	_, err := w.api.UpdateTransaction(ctx, orig.ID, payload)
	return err
}

// splitSaved reports whether YNAB's reply holds exactly the requested split:
// the same number of live subtransactions with the same (category, amount)
// pairs, in any order, summing to the parent.
func splitSaved(saved ynab.Transaction, job ChargeJob) bool {
	want := -job.Charge.AmountCents * milliPerCent
	if saved.Amount != want {
		return false
	}
	type part struct {
		cat    string
		amount int64
	}
	pending := make(map[part]int, len(job.Splits))
	for _, s := range job.Splits {
		pending[part{s.CategoryID, s.AmountMilli}]++
	}
	var live int
	var sum int64
	for _, s := range saved.SubTransactions {
		if s.Deleted {
			continue
		}
		live++
		sum += s.Amount
		p := part{amount: s.Amount}
		if s.CategoryID != nil {
			p.cat = *s.CategoryID
		}
		if pending[p] == 0 {
			return false
		}
		pending[p]--
	}
	return live == len(job.Splits) && sum == want
}

// isRejected reports a 400: YNAB refused the whole update.
func isRejected(err error) bool {
	var apiErr *ynab.APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest
}

// parentMemo appends the marker to the existing memo, never losing its text.
// An empty memo on a single-category charge gets the item names as its base.
func parentMemo(existing string, job ChargeJob) string {
	if existing == "" && len(job.Splits) == 1 {
		existing = job.Splits[0].Memo
	}
	return memo.AppendMarker(existing, job.Charge.Key)
}

func saveSubs(splits []splitter.Split) []ynab.SaveSubTransaction {
	out := make([]ynab.SaveSubTransaction, 0, len(splits))
	for _, s := range splits {
		out = append(out, ynab.SaveSubTransaction{Amount: s.AmountMilli, CategoryID: s.CategoryID, Memo: s.Memo})
	}
	return out
}

func sameCategory(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
