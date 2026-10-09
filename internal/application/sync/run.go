package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
)

// maxErrRunes bounds the error text kept in a Row or StopReason.
const maxErrRunes = 300

// run is the state of one Run: the summary so far and the lazily built
// Writer.
type run struct {
	o       *Orchestrator
	opts    Options
	cat     *catalog
	wcfg    Config
	since   time.Time
	txnFrom time.Time
	sum     Summary

	writer *Writer
	// loadErr is a failed transactions load (other than a rate limit); it is
	// kept so the load is attempted once per run, not once per charge.
	loadErr error
}

// slot is one card charge of an order; done rows are reported in charge order.
type slot struct {
	row  Row
	done bool
}

func (s *slot) set(status Status, txnID, note string) {
	s.row.Status, s.row.TxnID, s.row.Note, s.done = status, txnID, note, true
}

func (s *slot) fail(err error) {
	s.row.Status, s.row.Err, s.done = StatusFailed, shortErr(err), true
}

func (r *run) add(row Row) { r.sum.Rows = append(r.sum.Rows, row) }

func (r *run) processOrders(ctx context.Context) error {
	refs, err := r.o.d.Provider.Orders(ctx, r.since)
	switch {
	case err == nil:
	case r.stopIfBlocked(err):
		return nil
	case ctx.Err() != nil:
		return interrupted(ctx.Err())
	default:
		return fmt.Errorf("list Walmart orders: %w", err)
	}
	if r.opts.Max > 0 && len(refs) > r.opts.Max {
		refs = refs[:r.opts.Max]
	}
	r.o.log.Info("orders to process", "count", len(refs))
	for i, ref := range refs {
		if i > 0 {
			if err := r.o.d.Sleep(ctx, PacingDelay); err != nil {
				return interrupted(err)
			}
		}
		stop, err := r.processRef(ctx, ref)
		if stop || err != nil {
			return err
		}
	}
	return nil
}

// processRef fetches one order and processes it. stop means end the run.
func (r *run) processRef(ctx context.Context, ref order.OrderRef) (stop bool, err error) {
	o, err := r.o.d.Provider.Order(ctx, ref)
	switch {
	case err == nil:
		return r.processOrder(ctx, o)
	case r.stopIfBlocked(err):
		return true, nil
	case ctx.Err() != nil:
		return true, interrupted(ctx.Err())
	}
	if !strings.Contains(err.Error(), ref.ID) {
		err = fmt.Errorf("order %s: %w", ref.ID, err)
	}
	r.o.log.Warn("an order could not be fetched; continuing")
	r.o.log.Debug("order fetch failed", "order_id", ref.ID, "error", err)
	r.add(Row{OrderDisplayID: ref.ID, Status: StatusFailed, Err: shortErr(err)})
	return false, nil
}

func (r *run) processOrder(ctx context.Context, o order.Order) (bool, error) {
	display := o.DisplayID
	if display == "" {
		display = o.ID
	}
	r.o.log.Debug("order fetched", "order_id", o.ID, "display_id", display,
		"charges", len(o.Charges), "skipped", len(o.Skipped))
	for _, s := range o.Skipped {
		r.add(skippedRow(display, s))
	}
	if len(o.Charges) == 0 {
		if len(o.Skipped) == 0 {
			r.add(Row{OrderDisplayID: display, Status: StatusSkipped, Note: "order has no card charges"})
		}
		return false, nil
	}
	slots := make([]slot, len(o.Charges))
	needWork := false
	for i, c := range o.Charges {
		slots[i].row = Row{OrderDisplayID: display, AmountCents: c.AmountCents}
		if r.opts.Force || !r.checkStore(ctx, c, &slots[i]) {
			needWork = true
		}
	}
	defer r.flush(slots)
	if !needWork {
		return false, nil
	}
	// Load the transactions first: if that fails, no LLM tokens are spent.
	if _, err := r.writerFor(ctx); err != nil {
		return r.failPending(ctx, slots, err)
	}
	items, catErr := r.categorize(ctx, o)
	if catErr != nil && ctx.Err() != nil {
		return true, interrupted(ctx.Err())
	}
	if catErr != nil {
		for i := range slots {
			if !slots[i].done {
				slots[i].fail(catErr)
			}
		}
		return false, nil
	}
	return r.processCharges(ctx, display, o, items, slots)
}

// failPending applies a transactions-load error to every charge still
// pending: each fails, or the run stops (rate limit, cancellation).
func (r *run) failPending(ctx context.Context, slots []slot, err error) (bool, error) {
	for i := range slots {
		if slots[i].done {
			continue
		}
		if stop, stopErr := r.chargeError(ctx, err, &slots[i]); stop || stopErr != nil {
			return stop, stopErr
		}
	}
	return false, nil
}

// checkStore reports whether the charge is finished without more work:
// already recorded, or the store could not be read (a Failed row).
func (r *run) checkStore(ctx context.Context, c order.Charge, s *slot) bool {
	rec, found, err := r.o.d.Store.GetCharge(ctx, c.Key)
	switch {
	case err != nil:
		s.fail(fmt.Errorf("charge %s: cannot check whether it was already processed: %w", c.Key, err))
		return true
	case found:
		s.set(StatusAlreadyProcessed, rec.YNABTxnID, "recorded as "+rec.Outcome)
		return true
	default:
		return false
	}
}

// categorize assigns the order's items once for all of its charges.
func (r *run) categorize(ctx context.Context, o order.Order) ([]splitter.Item, error) {
	if len(o.Items) == 0 {
		return nil, errors.New("order has no items to split the charge by")
	}
	overrides := r.o.d.Config.YNAB.CategoryOverrides
	items, err := categorizeItems(ctx, r.o.d.Categorizer, o.Items, offeredCategories(r.cat.allowed(), overrides), overrides)
	if err != nil {
		r.o.log.Warn("categorization failed for an order; its charges fail")
		r.o.log.Debug("categorization failed", "order_id", o.ID, "error", err)
		return nil, fmt.Errorf("categorize items: %w", err)
	}
	return items, nil
}

// chargeError turns a writer or transactions error into a stop or a Failed
// row. A charge whose write already succeeded keeps its outcome.
func (r *run) chargeError(ctx context.Context, err error, s *slot) (bool, error) {
	switch {
	case errors.Is(err, ynab.ErrRateLimited):
		r.sum.StoppedEarly = &StopReason{Source: StopYNABRateLimit, Kind: StopKindYNABRateLimit,
			Reason: "YNAB rate limit", Err: shortErr(err)}
		r.o.log.Warn("YNAB rate limit reached; stopping the run")
		if s.done {
			s.row.Err = shortErr(err)
		}
		return true, nil
	case ctx.Err() != nil:
		return true, interrupted(ctx.Err())
	case s.done:
		s.row.Err = shortErr(err)
	default:
		s.fail(err)
	}
	r.o.log.Warn("a charge failed; continuing")
	r.o.log.Debug("charge failed", "error", err)
	return false, nil
}

// writerFor returns the run's Writer, loading the YNAB transactions on first
// use. A failed load is remembered and not retried.
func (r *run) writerFor(ctx context.Context) (*Writer, error) {
	if r.writer != nil {
		return r.writer, nil
	}
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	txns, err := r.o.d.Transactions.Load(ctx, r.o.d.Config.YNAB.PlanID, r.txnFrom)
	if err != nil {
		err = fmt.Errorf("load YNAB transactions: %w", err)
		if !errors.Is(err, ynab.ErrRateLimited) && ctx.Err() == nil {
			r.loadErr = err
		}
		return nil, err
	}
	w, err := NewWriter(r.o.d.Writes, r.o.d.Store, txns, r.wcfg)
	if err != nil {
		r.loadErr = err
		return nil, err
	}
	r.writer = w
	return w, nil
}

// splitError names unmapped categories (PRD §6.3); other splitter errors
// pass through.
func (r *run) splitError(err error) error {
	var unmapped *splitter.UnmappedCategoryError
	if errors.As(err, &unmapped) {
		return errors.New(r.cat.explainUnmapped(unmapped))
	}
	return err
}

func (r *run) views(splits []splitter.Split) []SplitView {
	out := make([]SplitView, 0, len(splits))
	for _, s := range splits {
		out = append(out, SplitView{Category: r.cat.name(s.CategoryID), CategoryID: s.CategoryID, AmountCents: -s.AmountMilli / milliPerCent})
	}
	return out
}

// stopIfBlocked records a Walmart block as the stop reason.
func (r *run) stopIfBlocked(err error) bool {
	if !errors.Is(err, order.ErrBlocked) {
		return false
	}
	reason, kind := "Walmart blocked", StopKindOther
	var b *order.BlockedError
	if errors.As(err, &b) {
		reason += ": " + b.Kind.String()
		kind = walmartStopKind(b.Kind)
	}
	r.sum.StoppedEarly = &StopReason{Source: StopWalmartBlocked, Kind: kind, Reason: reason, Err: shortErr(err)}
	r.o.log.Warn("Walmart blocked the session; stopping the run", "reason", reason)
	return true
}

func walmartStopKind(k order.BlockedKind) StopKind {
	switch k {
	case order.BotChallenge:
		return StopKindWalmartBotChallenge
	case order.StaleSession:
		return StopKindWalmartStaleSession
	case order.RateLimited:
		return StopKindWalmartRateLimited
	default:
		return StopKindOther
	}
}

func (r *run) flush(slots []slot) {
	for _, s := range slots {
		if s.done {
			r.add(s.row)
		}
	}
}

func skippedRow(display string, s order.SkippedCharge) Row {
	note := s.Reason.String()
	if s.Note != "" {
		note += ": " + s.Note
	}
	return Row{OrderDisplayID: display, AmountCents: s.AmountCents, Status: StatusSkipped, Note: note}
}

func interrupted(err error) error { return fmt.Errorf("run interrupted: %w", err) }

// shortErr keeps a row's error readable; the errors it receives never carry
// secrets (the YNAB client scrubs its token, the Walmart adapter its cookies).
func shortErr(err error) string {
	msg := []rune(err.Error())
	if len(msg) <= maxErrRunes {
		return string(msg)
	}
	return string(msg[:maxErrRunes]) + "..."
}
