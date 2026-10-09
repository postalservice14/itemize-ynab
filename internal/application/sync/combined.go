package sync

import (
	"context"
	"fmt"

	"github.com/postalservice14/itemize-ynab/internal/domain/matcher"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
)

// CombinedMatch is one YNAB transaction that pays several of an order's
// charges together. Jobs are indices into the slice given to PlanCombined.
type CombinedMatch struct {
	TxnID string
	Jobs  []int
}

// PlanCombined finds the transactions that pay two or more of jobs (one
// order's pending charges) as a single amount, and claims them. Jobs that
// match a transaction on their own are left out: Process handles them.
func (w *Writer) PlanCombined(jobs []ChargeJob) []CombinedMatch {
	var idx []int
	var charges []matcher.Charge
	for i, j := range jobs {
		c := w.matcherCharge(j)
		if matcher.Match(c, w.txns, w.claimed, w.cfg.MatchOptions).Status == matcher.NoMatch {
			idx = append(idx, i)
			charges = append(charges, c)
		}
	}
	groups := matcher.MatchCombined(charges, w.txns, w.claimed, w.cfg.MatchOptions)
	out := make([]CombinedMatch, 0, len(groups))
	for _, g := range groups {
		matcher.Claim(w.claimed, g.Txn.ID)
		m := CombinedMatch{TxnID: g.Txn.ID}
		for _, ci := range g.Charges {
			m.Jobs = append(m.Jobs, idx[ci])
		}
		w.log.Debug("charges paid by one transaction", "txn_id", g.Txn.ID, "charges", len(m.Jobs))
		out = append(out, m)
	}
	return out
}

// CombineJobs builds the job for charges one transaction pays together: the
// summed amount under the first member's key (the memo marker), date, order
// and account, with splits built for that sum. Every member is recorded.
func CombineJobs(members []ChargeJob, splits []splitter.Split) ChargeJob {
	job := members[0]
	job.Splits = splits
	job.Members = make([]order.Charge, 0, len(members))
	job.Charge.AmountCents = 0
	for _, m := range members {
		job.Members = append(job.Members, m.Charge)
		job.Charge.AmountCents += m.Charge.AmountCents
	}
	return job
}

// ProcessCombined writes a combined job onto the transaction PlanCombined
// matched to it, exactly as Process writes a single matched charge.
func (w *Writer) ProcessCombined(ctx context.Context, job ChargeJob, txnID string) (Result, error) {
	if err := validateJob(job); err != nil {
		return Result{Key: job.Charge.Key}, err
	}
	orig, ok := w.byID[txnID]
	if !ok {
		return Result{Key: job.Charge.Key}, fmt.Errorf("charge %s: combined match: unknown transaction %s", job.Charge.Key, txnID)
	}
	res := Result{Key: job.Charge.Key, Splits: job.Splits, DryRun: w.cfg.DryRun}
	return w.processMatched(ctx, job, orig, res)
}
