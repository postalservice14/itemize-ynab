package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
	"github.com/postalservice14/itemize-ynab/internal/domain/matcher"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

// PacingDelay is the wait between consecutive Walmart order fetches.
const PacingDelay = 2 * time.Second

// ReadAPI is the slice of the YNAB client the orchestrator reads with;
// *ynab.Client satisfies it.
type ReadAPI interface {
	ListCategories(ctx context.Context) ([]ynab.Category, error)
	ListAccounts(ctx context.Context) ([]ynab.Account, error)
}

// ItemCategorizer assigns every item one category name; *categorizer.Categorizer
// satisfies it.
type ItemCategorizer interface {
	Categorize(ctx context.Context, items []order.Item, allowed []string) ([]categorizer.Assignment, error)
}

// TxnLoader loads the run's candidate YNAB transactions; *TransactionSource
// satisfies it.
type TxnLoader interface {
	Load(ctx context.Context, planID string, from time.Time) ([]ynab.Transaction, error)
}

// Deps are the orchestrator's collaborators.
type Deps struct {
	Provider     order.OrderProvider
	Categorizer  ItemCategorizer
	YNAB         ReadAPI
	Writes       WriteAPI
	Transactions TxnLoader
	Store        ChargeStore
	Config       config.Config
	// Now is the clock; required.
	Now func() time.Time
	// Sleep waits d or until ctx is done; nil means SleepContext.
	Sleep  func(ctx context.Context, d time.Duration) error
	Logger *slog.Logger
}

// Options tune one Run.
type Options struct {
	// Days is how far back to list orders.
	Days int
	// Max caps the orders processed; 0 means no limit. The provider lists
	// newest first, so Max keeps the newest orders.
	Max    int
	DryRun bool
	Force  bool
}

// Orchestrator runs one sync: Walmart orders, categorize, split, write.
type Orchestrator struct {
	d   Deps
	log *slog.Logger
}

// NewOrchestrator checks the dependencies and builds an Orchestrator.
func NewOrchestrator(d Deps) (*Orchestrator, error) {
	switch {
	case d.Provider == nil, d.Categorizer == nil, d.YNAB == nil, d.Writes == nil,
		d.Transactions == nil, d.Store == nil:
		return nil, errors.New("sync: orchestrator is missing a dependency")
	case d.Now == nil:
		return nil, errors.New("sync: orchestrator needs a clock (Deps.Now)")
	}
	if d.Sleep == nil {
		d.Sleep = SleepContext
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Orchestrator{d: d, log: d.Logger}, nil
}

// SleepContext waits d, returning early with ctx.Err() when ctx is done.
func SleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run performs one sync (PRD §6.3-§6.8):
//
//  1. Preflight, before any Walmart call or write: one YNAB categories call;
//     one accounts call only when the config maps card accounts; the config
//     cross-check (ValidateConfig) and the writer settings. Any failure is
//     returned as an error: config.ErrInvalid or a YNAB 401/403 classify as
//     exit code 1, a YNAB 429 as 3 (see ExitCodeForError).
//  2. List orders placed since Now-Days and keep the first Max refs (the
//     provider lists newest first, so Max keeps the newest orders). Fetch
//     them one by one, waiting PacingDelay between fetches.
//  3. Per order: report skipped ledger entries; per card charge check the
//     store first (unless Force), so already processed charges cost no LLM
//     call; categorize the order's items once if any charge still needs
//     work; build each charge's splits from the CHARGE amount; hand it to
//     the Writer. The YNAB transactions are loaded lazily, once, on the first
//     charge that reaches the Writer.
//
// A YNAB rate limit or a Walmart block stops the run: the partial Summary is
// returned with StoppedEarly set and a nil error. Other per-order and
// per-charge failures (including LLM rate limits and auth errors) become
// Failed rows and the run continues. A cancelled ctx returns the partial
// Summary with an error matching ctx.Err().
//
// A dry run follows the same flow with the Writer in dry-run mode: no YNAB
// writes and no charge records, but the categorizer is still called (a dry
// run spends LLM tokens) and the transaction source may refresh its local
// cache of YNAB transactions.
func (o *Orchestrator) Run(ctx context.Context, opts Options) (Summary, error) {
	if opts.Days < 0 || opts.Max < 0 {
		return Summary{DryRun: opts.DryRun}, fmt.Errorf("%w: days (%d) and max (%d) must not be negative",
			config.ErrInvalid, opts.Days, opts.Max)
	}
	r, err := o.prepare(ctx, opts)
	if err != nil {
		return Summary{DryRun: opts.DryRun}, err
	}
	o.log.Info("sync started", "days", opts.Days, "max", opts.Max, "dry_run", opts.DryRun, "force", opts.Force)
	err = r.processOrders(ctx)
	o.log.Info("sync finished", "rows", len(r.sum.Rows), "stopped_early", r.sum.StoppedEarly != nil)
	return r.sum, err
}

// prepare runs the preflight: writer settings, categories, accounts and the
// config cross-check.
func (o *Orchestrator) prepare(ctx context.Context, opts Options) (*run, error) {
	wcfg, err := o.writerConfig(opts)
	if err != nil {
		return nil, err
	}
	y := o.d.Config.YNAB
	cats, err := o.d.YNAB.ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch YNAB categories: %w", err)
	}
	var accounts []ynab.Account
	if len(y.Accounts) > 0 {
		accounts, err = o.d.YNAB.ListAccounts(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetch YNAB accounts: %w", err)
		}
	}
	if err := ValidateConfig(y, cats, accounts); err != nil {
		return nil, fmt.Errorf("config does not match the YNAB plan: %w", err)
	}
	since := o.d.Now().AddDate(0, 0, -opts.Days)
	txnFrom := since.AddDate(0, 0, -(y.MatchWindow.DaysBefore + 1))
	wcfg.LoadedFrom = txnFrom
	return &run{
		o:       o,
		opts:    opts,
		cat:     newCatalog(cats),
		wcfg:    wcfg,
		since:   since,
		txnFrom: txnFrom,
		sum:     Summary{DryRun: opts.DryRun},
	}, nil
}

// writerConfig builds and checks the Writer settings up front so a bad value
// fails before any Walmart call.
func (o *Orchestrator) writerConfig(opts Options) (Config, error) {
	y := o.d.Config.YNAB
	cfg, err := withConfigDefaults(Config{
		FlagColor: y.FlagColor,
		MatchOptions: matcher.Options{
			DaysBefore: y.MatchWindow.DaysBefore,
			DaysAfter:  y.MatchWindow.DaysAfter,
		},
		SplitInPlace: Mode(y.SplitInPlace),
		DryRun:       opts.DryRun,
		Force:        opts.Force,
		Now:          o.d.Now,
		Logger:       o.log,
	})
	if err != nil {
		return cfg, fmt.Errorf("%w: %w", config.ErrInvalid, err)
	}
	return cfg, nil
}
