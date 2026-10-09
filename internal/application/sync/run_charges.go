package sync

import (
	"context"
	"fmt"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
)

// processCharges writes an order's pending charges: first the groups that one
// transaction pays together, then each remaining charge on its own. stop
// means end the run (rate limit or cancellation).
func (r *run) processCharges(ctx context.Context, display string, charges []order.Charge, items []splitter.Item, slots []slot) (bool, error) {
	jobs := make([]ChargeJob, len(charges))
	var pending []int
	for i, c := range charges {
		if slots[i].done {
			continue
		}
		if job, ok := r.buildJob(display, c, len(charges), items, &slots[i]); ok {
			jobs[i] = job
			pending = append(pending, i)
		}
	}
	w, err := r.writerFor(ctx)
	if err != nil {
		return r.failPending(ctx, slots, err)
	}
	pendingJobs := make([]ChargeJob, len(pending))
	for k, i := range pending {
		pendingJobs[k] = jobs[i]
	}
	for _, m := range w.PlanCombined(pendingJobs) {
		members := make([]int, len(m.Jobs))
		for k, j := range m.Jobs {
			members[k] = pending[j]
		}
		if stop, err := r.processGroup(ctx, w, m.TxnID, members, jobs, items, slots); stop || err != nil {
			return stop, err
		}
	}
	for _, i := range pending {
		if slots[i].done {
			continue
		}
		if stop, err := r.writeCharge(ctx, w, jobs[i], &slots[i]); stop || err != nil {
			return stop, err
		}
	}
	return false, nil
}

// buildJob builds one charge's splits; a split failure fails its slot.
func (r *run) buildJob(display string, c order.Charge, orderCharges int, items []splitter.Item, s *slot) (ChargeJob, bool) {
	splits, err := splitter.BuildSplits(c.AmountCents, items, r.cat.resolve)
	if err != nil {
		s.fail(r.splitError(err))
		return ChargeJob{}, false
	}
	s.row.Splits = r.views(splits)
	acct, _ := order.ResolveAccount(c.LastFour, r.o.d.Config.YNAB.Accounts)
	return ChargeJob{Charge: c, OrderDisplayID: display, AccountID: acct, Splits: splits, OrderCharges: orderCharges}, true
}

// writeCharge writes one charge on its own.
func (r *run) writeCharge(ctx context.Context, w *Writer, job ChargeJob, s *slot) (bool, error) {
	r.o.log.Debug("writing charge", "key", job.Charge.Key, "amount_cents", job.Charge.AmountCents, "splits", len(job.Splits))
	res, err := w.Process(ctx, job)
	if res.Outcome != "" {
		s.set(Status(res.Outcome), res.TxnID, res.Note)
	}
	if err != nil {
		return r.chargeError(ctx, err, s)
	}
	return false, nil
}

// processGroup writes the charges at members, which transaction txnID pays
// together, as one split of their sum. Each member row keeps its own share.
func (r *run) processGroup(ctx context.Context, w *Writer, txnID string, members []int, jobs []ChargeJob,
	items []splitter.Item, slots []slot,
) (bool, error) {
	group := make([]ChargeJob, 0, len(members))
	var total int64
	for _, i := range members {
		group = append(group, jobs[i])
		total += jobs[i].Charge.AmountCents
	}
	splits, err := splitter.BuildSplits(total, items, r.cat.resolve)
	if err != nil {
		for _, i := range members {
			slots[i].fail(r.splitError(err))
		}
		return false, nil
	}
	job := CombineJobs(group, splits)
	r.o.log.Debug("writing combined charges", "key", job.Charge.Key, "charges", len(members), "amount_cents", total)
	res, err := w.ProcessCombined(ctx, job, txnID)
	note := combinedNote(len(members)-1, total)
	for _, i := range members {
		s := &slots[i]
		if res.Outcome != "" {
			s.set(Status(res.Outcome), res.TxnID, joinNotes(note, res.Note))
		}
		if err != nil {
			if stop, stopErr := r.chargeError(ctx, err, s); stop || stopErr != nil {
				return stop, stopErr
			}
		}
	}
	return false, nil
}

func combinedNote(others int, totalCents int64) string {
	noun := "charges"
	if others == 1 {
		noun = "charge"
	}
	return fmt.Sprintf("paid together with %d other %s by one $%d.%02d transaction", others, noun, totalCents/100, totalCents%100)
}
