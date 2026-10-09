package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/matcher"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/storage"
)

// DefaultPayee is the payee name of sibling and staged transactions.
const DefaultPayee = "Walmart"

// DefaultStageMaxAgeDays is how old a charge may be and still be pre-staged.
const DefaultStageMaxAgeDays = 10

// WriteAPI is the slice of the YNAB client the writer needs; *ynab.Client
// satisfies it.
type WriteAPI interface {
	UpdateTransaction(ctx context.Context, id string, t ynab.SaveTransaction) (ynab.Transaction, error)
	CreateTransaction(ctx context.Context, t ynab.SaveTransaction) (ynab.Transaction, error)
}

// ChargeStore is the slice of the store the writer needs; *storage.Store
// satisfies it.
type ChargeStore interface {
	GetCharge(ctx context.Context, key string) (storage.ChargeRecord, bool, error)
	RecordCharge(ctx context.Context, rec storage.ChargeRecord) error
}

// Config tunes the writer.
type Config struct {
	// FlagColor flags the sibling split and the original it stands in for.
	FlagColor string
	// Payee names sibling and staged transactions; default DefaultPayee.
	Payee        string
	MatchOptions matcher.Options
	// SplitInPlace is the split-in-place policy; empty means ModeAuto.
	SplitInPlace Mode
	// DryRun decides every outcome but writes nothing to YNAB or the store.
	DryRun bool
	// Force reprocesses charges the store already records, except
	// needs_manual_match ones (Skipped: a rerun would create a duplicate
	// sibling, because the flagged original carries no marker). For the other
	// outcomes only the memo marker protects against a duplicate write: the
	// matcher excludes marked transactions. Force does not protect a
	// needs_manual_match charge whose record is gone (a lost database).
	Force bool
	// StageMaxAgeDays bounds pre-staging; default DefaultStageMaxAgeDays.
	StageMaxAgeDays int
	// LoadedFrom is the lower date bound of the transactions the writer was
	// given. A charge whose match window starts before it is not pre-staged:
	// the same-amount safety check cannot see the missing days. Zero means
	// no bound.
	LoadedFrom time.Time
	// Now is the clock; required.
	Now    func() time.Time
	Logger *slog.Logger
}

// Writer writes charges to YNAB, one Process call per charge. It holds the
// per-run state (claimed transactions, whether split-in-place is switched
// off), so use one Writer per run. It is not safe for concurrent use.
type Writer struct {
	api   WriteAPI
	store ChargeStore
	cfg   Config
	log   *slog.Logger

	txns    []matcher.Txn
	byID    map[string]ynab.Transaction
	claimed map[string]bool
	// splitOff is set in ModeAuto after split-in-place is rejected once.
	splitOff bool
}

// NewWriter builds a writer over the run's candidate transactions (from
// TransactionSource.Load).
func NewWriter(api WriteAPI, store ChargeStore, txns []ynab.Transaction, cfg Config) (*Writer, error) {
	cfg, err := withConfigDefaults(cfg)
	if err != nil {
		return nil, err
	}
	w := &Writer{
		api:     api,
		store:   store,
		cfg:     cfg,
		log:     cfg.Logger,
		txns:    make([]matcher.Txn, 0, len(txns)),
		byID:    make(map[string]ynab.Transaction, len(txns)),
		claimed: map[string]bool{},
	}
	for _, t := range txns {
		w.txns = append(w.txns, ToMatcherTxn(t))
		w.byID[t.ID] = t
	}
	return w, nil
}

func withConfigDefaults(cfg Config) (Config, error) {
	if cfg.Now == nil {
		return cfg, errors.New("sync: writer needs a clock (Config.Now)")
	}
	if cfg.FlagColor == "" {
		return cfg, errors.New("sync: writer needs a flag color (Config.FlagColor)")
	}
	mode, err := ParseMode(string(cfg.SplitInPlace))
	if err != nil {
		return cfg, err
	}
	cfg.SplitInPlace = mode
	if cfg.Payee == "" {
		cfg.Payee = DefaultPayee
	}
	if cfg.StageMaxAgeDays <= 0 {
		cfg.StageMaxAgeDays = DefaultStageMaxAgeDays
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return cfg, nil
}

// Process handles one charge: validate, check idempotency, match, then write
// (PRD §6.4-§6.6). It returns an error, with nothing recorded unless a write
// already succeeded, when the job is invalid, the store cannot be read, or a
// YNAB write fails; errors.Is(err, ynab.ErrRateLimited) means stop the run.
// The Result is meaningful whenever Outcome is set, even alongside an error.
func (w *Writer) Process(ctx context.Context, job ChargeJob) (Result, error) {
	if err := validateJob(job); err != nil {
		return Result{Key: job.Charge.Key}, err
	}
	res := Result{Key: job.Charge.Key, Splits: job.Splits, DryRun: w.cfg.DryRun}
	res, done, err := w.checkProcessed(ctx, res)
	if err != nil || done {
		return res, err
	}
	m := matcher.Match(w.matcherCharge(job), w.txns, w.claimed, w.cfg.MatchOptions)
	switch m.Status {
	case matcher.Matched:
		matcher.Claim(w.claimed, m.Txn.ID)
		return w.processMatched(ctx, job, w.byID[m.Txn.ID], res)
	case matcher.Ambiguous:
		res.Outcome = Skipped
		res.Note = fmt.Sprintf("ambiguous match: candidates %s tie across accounts", strings.Join(candidateIDs(m.Candidates), ", "))
		w.log.Info("charge skipped", "key", res.Key, "reason", res.Note)
		return res, nil
	default:
		return w.stage(ctx, job, res)
	}
}

// checkProcessed consults the idempotency record. done means stop with res.
// Without Force a recorded charge is AlreadyProcessed. With Force a recorded
// charge is processed again (the memo marker still keeps marked transactions
// out of matching), except a needs_manual_match one: its original is flagged
// but carries no marker, so a rerun would create a duplicate sibling.
func (w *Writer) checkProcessed(ctx context.Context, res Result) (Result, bool, error) {
	rec, found, err := w.store.GetCharge(ctx, res.Key)
	if err != nil {
		return res, true, fmt.Errorf("charge %s: cannot check whether it was already processed: %w", res.Key, err)
	}
	switch {
	case !found:
		return res, false, nil
	case !w.cfg.Force:
		res.Outcome = AlreadyProcessed
		return res, true, nil
	case rec.Outcome == string(NeedsManualMatch):
		res.Outcome = Skipped
		res.Note = fmt.Sprintf("not forced: sibling split %s already exists for this charge and re-running "+
			"would create a duplicate sibling; resolve or delete the sibling in YNAB first", rec.YNABTxnID)
		w.log.Warn("forced rerun refused for a needs_manual_match charge", "key", res.Key, "sibling_id", rec.YNABTxnID)
		return res, true, nil
	default:
		return res, false, nil
	}
}

func (w *Writer) matcherCharge(job ChargeJob) matcher.Charge {
	return matcher.Charge{
		Key:         job.Charge.Key,
		AmountCents: job.Charge.AmountCents,
		Date:        job.Charge.Date,
		AccountID:   job.AccountID,
	}
}

// record stores a finished outcome after a successful write, once per charge
// the job stands for. A failure is only reported: for a transaction that
// carries the memo marker, the marker still keeps it out of matching. A
// sibling split is different: the original it stands in for carries no
// marker, so a rerun creates a second sibling.
func (w *Writer) record(ctx context.Context, job ChargeJob, res *Result) {
	charges := job.Members
	if len(charges) == 0 {
		charges = []order.Charge{job.Charge}
	}
	for _, c := range charges {
		err := w.store.RecordCharge(ctx, storage.ChargeRecord{
			Key:       c.Key,
			OrderID:   c.OrderID,
			YNABTxnID: res.TxnID,
			Outcome:   string(res.Outcome),
			CreatedAt: w.cfg.Now(),
		})
		w.logRecord(c.Key, err, res)
	}
}

func (w *Writer) logRecord(key string, err error, res *Result) {
	switch {
	case err == nil:
		w.log.Info("charge written", "key", key, "outcome", res.Outcome, "txn_id", res.TxnID)
	case res.Outcome == NeedsManualMatch:
		w.log.Warn("sibling split created but not recorded locally; a rerun will create a second sibling, delete one by hand",
			"key", key, "sibling_id", res.TxnID, "error", err)
		res.Note = joinNotes(res.Note, fmt.Sprintf("sibling %s exists in YNAB but was not recorded locally: "+
			"a rerun will create a second sibling, so delete one of them by hand (%v)", res.TxnID, err))
	default:
		w.log.Warn("charge written to YNAB but not recorded locally; the memo marker still prevents a duplicate",
			"key", key, "txn_id", res.TxnID, "error", err)
		res.Note = joinNotes(res.Note, "not recorded locally: "+err.Error())
	}
}

// writeErr wraps a failed YNAB write with the charge and transaction. The
// client's errors never carry the token.
func writeErr(key, action, txnID string, err error) error {
	if txnID == "" {
		return fmt.Errorf("charge %s: %s: %w", key, action, err)
	}
	return fmt.Errorf("charge %s: %s (transaction %s): %w", key, action, txnID, err)
}

func candidateIDs(txns []matcher.Txn) []string {
	ids := make([]string, 0, len(txns))
	for _, t := range txns {
		ids = append(ids, t.ID)
	}
	return ids
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
