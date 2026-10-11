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

// noMatchNote explains a charge with no bank transaction to write to yet. It is
// skipped, never created, and retried next run.
const noMatchNote = "no matching bank transaction yet; retried next run"

// WriteAPI is the slice of the YNAB client the writer needs; *ynab.Client
// satisfies it. It can only update existing transactions.
type WriteAPI interface {
	UpdateTransaction(ctx context.Context, id string, t ynab.SaveTransaction) (ynab.Transaction, error)
}

// ChargeStore is the slice of the store the writer needs; *storage.Store
// satisfies it.
type ChargeStore interface {
	GetCharge(ctx context.Context, key string) (storage.ChargeRecord, bool, error)
	RecordCharge(ctx context.Context, rec storage.ChargeRecord) error
}

// Config tunes the writer.
type Config struct {
	// FlagColor flags a transaction that needs a manual split.
	FlagColor    string
	MatchOptions matcher.Options
	// SplitInPlace is the split-in-place policy; empty means ModeAuto.
	SplitInPlace Mode
	// DryRun decides every outcome but writes nothing to YNAB or the store.
	DryRun bool
	// Force reprocesses charges the store already records. Only the memo
	// marker then protects against a duplicate write: the matcher excludes
	// marked transactions.
	Force bool
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
		res.Outcome, res.Note = Skipped, noMatchNote
		if job.Charge.Date.IsZero() {
			res.Note = "no usable date for the charge"
		}
		w.log.Info("charge skipped", "key", res.Key, "reason", res.Note)
		return res, nil
	}
}

// checkProcessed consults the idempotency record. done means stop with res.
// Without Force a recorded charge is AlreadyProcessed; with Force it is
// processed again, and the memo marker keeps marked transactions out of
// matching.
func (w *Writer) checkProcessed(ctx context.Context, res Result) (Result, bool, error) {
	_, found, err := w.store.GetCharge(ctx, res.Key)
	if err != nil {
		return res, true, fmt.Errorf("charge %s: cannot check whether it was already processed: %w", res.Key, err)
	}
	if found && !w.cfg.Force {
		res.Outcome = AlreadyProcessed
		return res, true, nil
	}
	return res, false, nil
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
// the job stands for. A failure is only reported: every written transaction
// carries the memo marker, which still keeps it out of matching.
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
	if err == nil {
		w.log.Info("charge written", "key", key, "outcome", res.Outcome, "txn_id", res.TxnID)
		return
	}
	w.log.Warn("charge written to YNAB but not recorded locally; the memo marker still prevents a duplicate",
		"key", key, "txn_id", res.TxnID, "error", err)
	res.Note = joinNotes(res.Note, "not recorded locally: "+err.Error())
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
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}
