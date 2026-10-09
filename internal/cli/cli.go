// Package cli parses commands, wires dependencies and maps errors to exit
// codes. Business logic lives elsewhere.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/application/sync"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
	"github.com/postalservice14/itemize-ynab/internal/version"
)

// Exit codes (see AGENTS.md).
const (
	ExitOK          = 0
	ExitConfigAuth  = 1
	ExitSomeFailed  = 2
	ExitRateLimited = 3
)

const usageText = `usage: itemize-ynab [-config path] <command>

commands:
  version                            print the version
  ynab categories [-eligible]        list YNAB categories (name, group, id)
  ynab accounts                      list YNAB accounts (name, id, type)
  ynab probe-split [-yes] [-categories "A,B"] <txn-id>
                                     try an in-place split on ONE real transaction
  walmart [-dry-run] [-days N] [-max N] [-force] [-verbose]
                                     split recent Walmart card charges by item category
                                     (-days default 14; -max 0 = no limit)
  walmart import-curl <file|->       write the Walmart cookie store from a browser
                                     "Copy as cURL" capture (no network call)

exit codes: 0 ok, 1 config/auth/usage error, 2 some charges failed,
3 stopped by a YNAB rate limit or a Walmart block.

The YNAB token is read from the YNAB_TOKEN environment variable via config.
`

// Env carries the process surroundings so commands are testable.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	// Stdin feeds `walmart import-curl -`; nil means no input.
	Stdin io.Reader
	// ClientOptions are passed to the YNAB client (tests point it at httptest).
	ClientOptions []ynab.Option
	// Getenv reads environment variables for the LLM selection; nil means
	// os.Getenv. (The YNAB token comes from the config file's ${VAR}.)
	Getenv func(string) string
	// Deps are the factories for everything that reaches outside the process.
	// Zero fields use the real implementation.
	Deps Deps
}

// errUsage marks a command-line mistake.
var errUsage = errors.New("usage error")

// ExitCode maps an error to the process exit code, the one place it is
// decided: a run's own verdict (some charges failed, stopped early) carries its
// code; a YNAB rate limit or a Walmart block is 3; configuration,
// authentication, usage, cancellation and any other fatal error is 1.
func ExitCode(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return sync.ExitCodeForError(err)
}

// Run executes the command line and returns the process exit code.
func Run(ctx context.Context, args []string, env Env) int {
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}
	err := run(ctx, args, env)
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, errUsage) {
		_, _ = fmt.Fprintf(env.Stderr, "error: %v\n\n%s", err, usageText)
	} else {
		_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
	}
	if errors.Is(err, ynab.ErrRateLimited) {
		_, _ = fmt.Fprintln(env.Stderr, "stopped: YNAB rate limit reached (200 requests per hour); try again later")
	}
	return ExitCode(err)
}

func run(ctx context.Context, args []string, env Env) error {
	fs := newFlagSet("itemize-ynab")
	configPath := fs.String("config", "config.yaml", "path to the config file")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("%w: no command given", errUsage)
	}
	switch rest[0] {
	case "version":
		_, _ = fmt.Fprintln(env.Stdout, version.String())
		return nil
	case "ynab":
		return runYNAB(ctx, *configPath, rest[1:], env)
	case "walmart":
		return runWalmart(ctx, *configPath, rest[1:], env)
	default:
		return fmt.Errorf("%w: unknown command %q", errUsage, rest[0])
	}
}

func runYNAB(ctx context.Context, configPath string, args []string, env Env) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: ynab needs a subcommand", errUsage)
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "categories", "accounts", "probe-split":
	default:
		return fmt.Errorf("%w: unknown ynab subcommand %q", errUsage, sub)
	}
	// Parse flags before loading config so a usage mistake needs no config.
	switch sub {
	case "categories":
		eligible := false
		fs := newFlagSet(sub)
		fs.BoolVar(&eligible, "eligible", false, "only categories the categorizer may use")
		if _, err := parseInterspersed(fs, subArgs); err != nil {
			return err
		}
		return withClient(configPath, env, func(c *ynab.Client, cfg *config.Config) error {
			return listCategories(ctx, c, env.Stdout, eligible, cfg.YNAB.ExcludeCategories)
		})
	case "accounts":
		if _, err := parseInterspersed(newFlagSet(sub), subArgs); err != nil {
			return err
		}
		return withClient(configPath, env, func(c *ynab.Client, _ *config.Config) error {
			return listAccounts(ctx, c, env.Stdout)
		})
	default:
		yes := false
		categoriesSpec := ""
		fs := newFlagSet(sub)
		fs.BoolVar(&yes, "yes", false, "confirm the write to the real transaction")
		fs.StringVar(&categoriesSpec, "categories", "", "two category names to split into, separated by a comma")
		pos, err := parseInterspersed(fs, subArgs)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return fmt.Errorf("%w: probe-split needs exactly one transaction ID", errUsage)
		}
		names, err := parseProbeCategories(categoriesSpec)
		if err != nil {
			return err
		}
		return withClient(configPath, env, func(c *ynab.Client, cfg *config.Config) error {
			return probeSplit(ctx, c, cfg.YNAB, env.Stdout, pos[0], yes, names)
		})
	}
}

func withClient(configPath string, env Env, fn func(*ynab.Client, *config.Config) error) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	client := ynab.NewClient(cfg.YNAB.Token.Reveal(), cfg.YNAB.PlanID, env.ClientOptions...)
	return fn(client, cfg)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseInterspersed parses flags that may appear before or after positional
// arguments and returns the positionals.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%w: %w", errUsage, err)
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// OSEnv is the Env for the real process.
func OSEnv() Env { return Env{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin} }
