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
func (r *run) processCharges(ctx context.Context, display string, o order.Order, items []splitter.Item, slots []slot) (bool, error) {
	plan, err := r.orderSplits(o, items)
	if err != nil {
		for i := range slots {
			if !slots[i].done {
				slots[i].fail(r.splitError(err))
			}
		}
		return false, nil
	}
	jobs := make([]ChargeJob, len(o.Charges))
	var pending []int
	for i, c := range o.Charges {
		if slots[i].done {
			continue
		}
		slots[i].row.Splits = r.views(plan[i])
		acct, _ := order.ResolveAccount(c.LastFour, r.o.d.Config.YNAB.Accounts)
		jobs[i] = ChargeJob{Charge: c, OrderDisplayID: display, AccountID: acct, Splits: plan[i], OrderCharges: len(o.Charges)}
		pending = append(pending, i)
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
		if stop, err := r.processGroup(ctx, w, m.TxnID, members, jobs, slots); stop || err != nil {
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

// orderSplits plans the splits of every charge of the order, written or not,
// so the plan is the same on every run. A separately charged tip is split by
// the items on its own (or goes to tip_category when one is set), and the
// other charges are filled from the items' category totals.
func (r *run) orderSplits(o order.Order, items []splitter.Item) ([][]splitter.Split, error) {
	plan := make([][]splitter.Split, len(o.Charges))
	tipIdx, isTip := order.TipCharge(o)
	var idx []int
	var amounts []int64
	for i, c := range o.Charges {
		if isTip && i == tipIdx {
			continue
		}
		idx = append(idx, i)
		amounts = append(amounts, c.AmountCents)
	}
	if isTip {
		tip, err := r.tipSplits(o.Charges[tipIdx].AmountCents, items)
		if err != nil {
			return nil, err
		}
		plan[tipIdx] = tip
	}
	if len(idx) == 0 {
		return plan, nil
	}
	splits, err := splitter.BuildOrderSplits(amounts, items, r.cat.resolve)
	if err != nil {
		return nil, err
	}
	for k, i := range idx {
		plan[i] = splits[k]
	}
	return plan, nil
}

// tipSplits splits a tip charge: all of it to tip_category when one is set,
// otherwise in proportion to the items' categories.
func (r *run) tipSplits(cents int64, items []splitter.Item) ([]splitter.Split, error) {
	tipCat := r.o.d.Config.YNAB.TipCategory
	if tipCat == "" {
		return splitter.BuildSplits(cents, items, r.cat.resolve)
	}
	id, ok := r.cat.resolve(tipCat)
	if !ok {
		return nil, &splitter.UnmappedCategoryError{Names: []string{tipCat}}
	}
	return []splitter.Split{{CategoryID: id, AmountMilli: -cents * milliPerCent, Memo: tipMemo}}, nil
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
// together, as one split: their planned shares merged. Each member row keeps
// its own share.
func (r *run) processGroup(ctx context.Context, w *Writer, txnID string, members []int, jobs []ChargeJob, slots []slot) (bool, error) {
	group := make([]ChargeJob, 0, len(members))
	lists := make([][]splitter.Split, 0, len(members))
	var total int64
	for _, i := range members {
		group = append(group, jobs[i])
		lists = append(lists, jobs[i].Splits)
		total += jobs[i].Charge.AmountCents
	}
	job := CombineJobs(group, splitter.MergeSplits(lists...))
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

// tipMemo is the memo of a tip charge written to tip_category.
const tipMemo = "Driver tip"

func combinedNote(others int, totalCents int64) string {
	noun := "charges"
	if others == 1 {
		noun = "charge"
	}
	return fmt.Sprintf("paid together with %d other %s by one $%d.%02d transaction", others, noun, totalCents/100, totalCents%100)
}
