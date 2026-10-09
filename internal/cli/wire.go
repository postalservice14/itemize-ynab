package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/postalservice14/itemize-ynab/internal/adapters/llm"
	"github.com/postalservice14/itemize-ynab/internal/adapters/walmart"
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/application/sync"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/lock"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/storage"
)

// lockSuffix names the run lock file next to the database.
const lockSuffix = ".lock"

// acquireRunLock takes the run lock (<database.path>.lock) for a whole sync
// run, dry runs included, so two runs never overlap. It never waits: a held
// lock is an error (exit code 1). The kernel drops the lock if the process
// dies, so there is no stale lock to clean up.
func acquireRunLock(dbPath string) (func(), error) {
	path := dbPath + lockSuffix
	release, err := lock.Acquire(path)
	switch {
	case err == nil:
		return release, nil
	case errors.Is(err, lock.ErrLocked):
		return nil, fmt.Errorf("another itemize-ynab run is in progress (lock: %s)", path)
	default:
		return nil, fmt.Errorf("take the run lock next to the database (setting database.path): %w", err)
	}
}

// withDefaults fills every nil factory with the real implementation.
func (d Deps) withDefaults() Deps {
	if d.NewProvider == nil {
		d.NewProvider = newWalmartProvider
	}
	if d.NewChat == nil {
		d.NewChat = newChatFromEnv
	}
	if d.NewStore == nil {
		d.NewStore = openStore
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = sync.SleepContext
	}
	return d
}

// buildOrchestrator assembles the run's collaborators. Everything that can be
// wrong with the local setup (LLM key, database, cookie file) is checked here,
// before the orchestrator makes its first YNAB or Walmart call. The returned
// cleanup closes the store.
func buildOrchestrator(ctx context.Context, cfg *config.Config, env Env, log *slog.Logger) (*sync.Orchestrator, func(), error) {
	deps := env.Deps.withDefaults()
	getenv := env.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	chat, err := deps.NewChat(getenv)
	if err != nil {
		return nil, nil, err
	}
	store, err := deps.NewStore(ctx, cfg.Database.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("open the database (setting database.path): %w", err)
	}
	provider, err := deps.NewProvider(cfg, log)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	client := ynab.NewClient(cfg.YNAB.Token.Reveal(), cfg.YNAB.PlanID, env.ClientOptions...)
	orch, err := sync.NewOrchestrator(sync.Deps{
		Provider:     provider,
		Categorizer:  categorizer.New(chat),
		YNAB:         client,
		Writes:       client,
		Transactions: sync.NewTransactionSource(client, store, log),
		Store:        store,
		Config:       *cfg,
		Now:          deps.Now,
		Sleep:        deps.Sleep,
		Logger:       log,
	})
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return orch, func() { _ = store.Close() }, nil
}

func newChatFromEnv(getenv func(string) string) (categorizer.ChatClient, error) {
	chat, _, err := llm.NewChatClientFromEnv(getenv)
	return chat, err
}

func openStore(ctx context.Context, path string) (Store, error) {
	return storage.Open(ctx, path)
}

// newWalmartProvider builds the real Walmart provider. Building the client
// makes no network call; an empty cookie store is rejected here so a missing
// or unreadable cookie file fails the run before any request is made.
func newWalmartProvider(cfg *config.Config, log *slog.Logger) (order.OrderProvider, error) {
	client, err := walmart.NewClient(cfg.Walmart.CookieFile, walmart.WithClientLogger(log.With("system", "walmart")))
	if err != nil {
		return nil, fmt.Errorf("create the Walmart client (setting walmart.cookie_file): %w", err)
	}
	if client.CookieCount() == 0 {
		return nil, fmt.Errorf("no usable Walmart cookies in the file named by walmart.cookie_file; "+
			"capture a request in your browser (Copy as cURL) and run `itemize-ynab walmart import-curl <file>`: %w", errNoCookies)
	}
	return walmart.New(client, walmart.WithLogger(log)), nil
}
