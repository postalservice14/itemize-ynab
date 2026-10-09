package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
)

const probeMarker = "[itemize:probe]"

// Verdict lines printed by probe-split.
const (
	verdictWorks    = "verdict: split-in-place WORKS"
	verdictRejected = "verdict: split-in-place REJECTED (400)"
	verdictIgnored  = "verdict: split-in-place IGNORED (200 but no subtransactions saved)"
)

// probeSplit tries to turn one real, non-split transaction into a split with a
// single PUT, reports what YNAB did and tries to undo it. It mutates the
// transaction, so it refuses to write unless yes is true.
func probeSplit(ctx context.Context, c *ynab.Client, planID string, w io.Writer, txnID string, yes bool) error {
	orig, err := c.GetTransaction(ctx, txnID)
	if err != nil {
		return err
	}
	if err := checkProbeable(orig); err != nil {
		return err
	}
	cats, err := c.ListCategories(ctx)
	if err != nil {
		return err
	}
	picked := ynab.EligibleCategories(cats)
	if len(picked) < 2 {
		return fmt.Errorf("probe-split needs at least 2 eligible categories in the plan, found %d", len(picked))
	}
	req := buildProbeRequest(orig, picked[0], picked[1])

	_, _ = fmt.Fprintf(w, "transaction %s: %s, %s, amount %d milliunits, memo %q\n",
		orig.ID, orig.Date, orig.PayeeName, orig.Amount, orig.Memo)
	_, _ = fmt.Fprintf(w, "will split into %q and %q\n", picked[0].Name, picked[1].Name)
	if !yes {
		return fmt.Errorf("%w: probe-split modifies a real transaction; re-run with -yes to proceed", errUsage)
	}

	if err := printRequest(w, planID, txnID, req); err != nil {
		return err
	}
	resp, err := c.UpdateTransaction(ctx, txnID, req)
	var apiErr *ynab.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest:
		_, _ = fmt.Fprintf(w, "response: %v\n", apiErr)
		_, _ = fmt.Fprintln(w, "the transaction was not changed")
		_, _ = fmt.Fprintln(w, verdictRejected)
		return nil
	case err != nil:
		_, _ = fmt.Fprintln(w, "the probe could not finish; check the transaction in YNAB (the write may not have happened)")
		return err
	}
	if err := printJSON(w, "response", resp); err != nil {
		return err
	}
	return verifyAndRevert(ctx, c, w, orig)
}

func checkProbeable(t ynab.Transaction) error {
	switch {
	case t.Deleted:
		return fmt.Errorf("transaction %s is deleted", t.ID)
	case t.IsSplit():
		return fmt.Errorf("transaction %s is already a split; pick a non-split transaction", t.ID)
	case t.IsTransfer():
		return fmt.Errorf("transaction %s is a transfer; pick a non-transfer transaction", t.ID)
	case t.Amount > -2 && t.Amount < 2:
		return fmt.Errorf("transaction %s has amount %d, too small to split in two", t.ID, t.Amount)
	}
	return nil
}

func buildProbeRequest(orig ynab.Transaction, a, b ynab.Category) ynab.SaveTransaction {
	memo := probeMarker
	if orig.Memo != "" {
		memo = orig.Memo + " " + probeMarker
	}
	first := orig.Amount / 2
	return ynab.SaveTransaction{
		Memo: &memo,
		SubTransactions: []ynab.SaveSubTransaction{
			{Amount: first, CategoryID: a.ID, Memo: "probe part 1"},
			{Amount: orig.Amount - first, CategoryID: b.ID, Memo: "probe part 2"},
		},
	}
}

func printRequest(w io.Writer, planID, txnID string, req ynab.SaveTransaction) error {
	_, _ = fmt.Fprintf(w, "request: PUT /plans/%s/transactions/%s\n", planID, txnID)
	return printJSON(w, "body", map[string]any{"transaction": req})
}

func printJSON(w io.Writer, label string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", label, err)
	}
	_, _ = fmt.Fprintf(w, "%s:\n%s\n", label, b)
	return nil
}

func verifyAndRevert(ctx context.Context, c *ynab.Client, w io.Writer, orig ynab.Transaction) error {
	after, err := c.GetTransaction(ctx, orig.ID)
	if err != nil {
		_, _ = fmt.Fprintln(w, "could not re-fetch the transaction to verify; check it in YNAB")
		return err
	}
	worked := after.IsSplit()
	_, _ = fmt.Fprintf(w, "follow-up GET: split=%t, memo %q\n", worked, after.Memo)

	verdict := verdictIgnored
	if worked {
		verdict = verdictWorks
	}
	reverted, err := revert(ctx, c, w, orig)
	if err != nil {
		_, _ = fmt.Fprintf(w, "revert failed; check transaction %s in YNAB\n%s\n", orig.ID, verdict)
		return err
	}
	switch {
	case worked && !reverted:
		_, _ = fmt.Fprintf(w, "WARNING: the transaction CANNOT be reverted through the API and is now a split.\n"+
			"Fix it in the YNAB app: open transaction %s and restore category %s and memo %q.\n",
			orig.ID, categoryLabel(orig.CategoryID), orig.Memo)
	case reverted:
		_, _ = fmt.Fprintln(w, "transaction reverted to its original category and memo")
	default:
		_, _ = fmt.Fprintln(w, "could not confirm the original category and memo were restored; check the transaction in YNAB")
	}
	_, _ = fmt.Fprintln(w, verdict)
	return nil
}

// revert puts the original category and memo back, falling back to a
// memo-only restore if YNAB refuses to touch a split, then re-reads the
// transaction to see whether it is really back to how it started.
func revert(ctx context.Context, c *ynab.Client, w io.Writer, orig ynab.Transaction) (bool, error) {
	restore := ynab.SaveTransaction{Memo: &orig.Memo}
	if orig.CategoryID != nil {
		restore.CategoryID = orig.CategoryID
	} else {
		restore.ClearCategory = true
	}
	_, err := c.UpdateTransaction(ctx, orig.ID, restore)
	var apiErr *ynab.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest {
		_, _ = fmt.Fprintf(w, "revert refused: %v\n", apiErr)
		_, err = c.UpdateTransaction(ctx, orig.ID, ynab.SaveTransaction{Memo: &orig.Memo})
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest {
			_, _ = fmt.Fprintf(w, "memo restore refused: %v\n", apiErr)
			return false, nil
		}
	}
	if err != nil {
		return false, err
	}
	now, err := c.GetTransaction(ctx, orig.ID)
	if err != nil {
		return false, err
	}
	return !now.IsSplit() && now.Memo == orig.Memo && sameCategory(now.CategoryID, orig.CategoryID), nil
}

func sameCategory(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func categoryLabel(id *string) string {
	if id == nil {
		return "(none)"
	}
	return *id
}
