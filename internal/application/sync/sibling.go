package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
)

// sibling creates a flagged, user-entered split next to the matched
// transaction and flags the original, for the user to match by hand.
func (w *Writer) sibling(ctx context.Context, job ChargeJob, orig ynab.Transaction, res Result) (Result, error) {
	if w.cfg.DryRun {
		res.Outcome = NeedsManualMatch
		res.Note = fmt.Sprintf("would create a flagged sibling split and flag transaction %s", orig.ID)
		return res, nil
	}
	created, err := w.api.CreateTransaction(ctx, w.siblingPayload(job, orig))
	if err != nil {
		return res, writeErr(res.Key, "create sibling split", "", err)
	}
	res.Outcome, res.TxnID = NeedsManualMatch, created.ID
	flag := w.cfg.FlagColor
	_, flagErr := w.api.UpdateTransaction(ctx, orig.ID, ynab.SaveTransaction{FlagColor: &flag})
	if flagErr != nil {
		res.Note = fmt.Sprintf("sibling %s created but flagging original %s failed: %v", created.ID, orig.ID, flagErr)
		w.log.Warn("sibling split created but the original could not be flagged",
			"key", res.Key, "sibling_id", created.ID, "txn_id", orig.ID, "error", flagErr)
	}
	// The sibling exists, so record the charge whatever happened to the flag:
	// a rerun must never create a second sibling.
	w.record(ctx, job, &res)
	if errors.Is(flagErr, ynab.ErrRateLimited) {
		return res, writeErr(res.Key, "flag original", orig.ID, flagErr)
	}
	return res, nil
}

func (w *Writer) siblingPayload(job ChargeJob, orig ynab.Transaction) ynab.SaveTransaction {
	// Equal to orig.Amount (the matcher requires it); taken from the charge
	// because that is what the splits were validated against.
	amount := -job.Charge.AmountCents * milliPerCent
	marker := memo.AppendMarker("", job.Charge.Key)
	flag := w.cfg.FlagColor
	approved := false
	return ynab.SaveTransaction{
		AccountID:       orig.AccountID,
		Date:            orig.Date,
		Amount:          &amount,
		PayeeName:       w.cfg.Payee,
		Memo:            &marker,
		FlagColor:       &flag,
		Approved:        &approved,
		SubTransactions: saveSubs(job.Splits),
	}
}
