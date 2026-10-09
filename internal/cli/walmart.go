package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/postalservice14/itemize-ynab/internal/application/sync"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

const (
	defaultDays      = 14
	importCurlSubcmd = "import-curl"
)

// walmartOpts are the parsed flags of the walmart command.
type walmartOpts struct {
	Days    int
	Max     int
	DryRun  bool
	Force   bool
	Verbose bool
}

// parseWalmartFlags parses the walmart flags (which may follow positional
// arguments) and returns the positionals. A usage mistake wraps errUsage.
func parseWalmartFlags(args []string) (walmartOpts, []string, error) {
	opts := walmartOpts{}
	fs := newFlagSet("walmart")
	fs.IntVar(&opts.Days, "days", defaultDays, "how many days back to look for orders")
	fs.IntVar(&opts.Max, "max", 0, "process at most this many orders, newest first (0 = no limit)")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "decide and print, but write nothing to YNAB")
	fs.BoolVar(&opts.Force, "force", false, "reprocess charges already recorded")
	fs.BoolVar(&opts.Verbose, "verbose", false, "debug logs and the proposed splits per charge")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return walmartOpts{}, nil, err
	}
	if opts.Days < 1 {
		return walmartOpts{}, nil, fmt.Errorf("%w: -days must be at least 1 (got %d)", errUsage, opts.Days)
	}
	if opts.Max < 0 {
		return walmartOpts{}, nil, fmt.Errorf("%w: -max must not be negative (got %d)", errUsage, opts.Max)
	}
	return opts, pos, nil
}

// exitError carries an exit code decided from a finished run's summary.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func runWalmart(ctx context.Context, configPath string, args []string, env Env) error {
	opts, pos, err := parseWalmartFlags(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return walmartSubcommand(configPath, opts, pos, env)
	}
	log := newLogger(env.Stderr, opts.Verbose)
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	release, err := acquireRunLock(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer release()
	orch, cleanup, err := buildOrchestrator(ctx, cfg, env, log)
	if err != nil {
		return err
	}
	defer cleanup()
	sum, err := orch.Run(ctx, sync.Options{Days: opts.Days, Max: opts.Max, DryRun: opts.DryRun, Force: opts.Force})
	return finishRun(env.Stdout, sum, err, opts.Verbose)
}

func walmartSubcommand(configPath string, opts walmartOpts, pos []string, env Env) error {
	if pos[0] != importCurlSubcmd {
		return fmt.Errorf("%w: unexpected argument %q", errUsage, pos[0])
	}
	if opts != (walmartOpts{Days: defaultDays}) {
		return fmt.Errorf("%w: %s takes no flags", errUsage, importCurlSubcmd)
	}
	if len(pos) != 2 {
		return fmt.Errorf("%w: %s needs exactly one argument: a file, or - for stdin", errUsage, importCurlSubcmd)
	}
	return runImportCurl(configPath, pos[1], env)
}

func newLogger(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// finishRun prints the summary and decides the error (hence the exit code).
// A partial summary is printed even when Run also failed (Ctrl-C), but not
// when it failed before doing anything.
func finishRun(stdout io.Writer, sum sync.Summary, runErr error, verbose bool) error {
	if runErr == nil || len(sum.Rows) > 0 || sum.StoppedEarly != nil {
		PrintSummary(stdout, sum, verbose)
	}
	if runErr != nil {
		return runErr
	}
	switch sum.ExitCode() {
	case sync.ExitStoppedEarly:
		return &exitError{code: sync.ExitStoppedEarly, msg: "stopped early: " + sum.StoppedEarly.Reason + "; see the summary above"}
	case sync.ExitSomeFailed:
		return &exitError{code: sync.ExitSomeFailed, msg: fmt.Sprintf("%d charge(s) failed; see the summary above", sum.Totals()[sync.StatusFailed])}
	default:
		return nil
	}
}
