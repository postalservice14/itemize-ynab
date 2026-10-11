package sync

import (
	"context"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
)

// manualSplitNote tells the user what a needs_manual_match row asks of them;
// the summary lists the planned split under the row.
const manualSplitNote = "YNAB did not take the split; flagged for you to split by hand as listed"

// flagForManualSplit flags the matched transaction and appends the marker to
// its memo, in one update, for the user to split by hand. The marker keeps it
// out of matching on later runs, even without the database. Nothing is
// recorded when the update fails, so the charge is retried next run.
func (w *Writer) flagForManualSplit(ctx context.Context, job ChargeJob, orig ynab.Transaction, res Result) (Result, error) {
	flag, marked := w.cfg.FlagColor, memo.AppendMarker(orig.Memo, job.Charge.Key)
	if _, err := w.api.UpdateTransaction(ctx, orig.ID, ynab.SaveTransaction{FlagColor: &flag, Memo: &marked}); err != nil {
		return res, writeErr(res.Key, "flag for a manual split", orig.ID, err)
	}
	res.Outcome, res.TxnID, res.Note = NeedsManualMatch, orig.ID, manualSplitNote
	w.record(ctx, job, &res)
	return res, nil
}
