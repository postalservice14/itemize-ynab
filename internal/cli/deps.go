package cli

import (
	"context"
	"log/slog"
	"time"

	"github.com/postalservice14/itemize-ynab/internal/application/sync"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

// Store is the persistence the walmart command needs; *storage.Store
// satisfies it through the adapter in wire.go.
type Store interface {
	sync.ChargeStore
	sync.TxnCache
	Close() error
}

// Deps are the factories behind the walmart command. Each field defaults to
// the real implementation, so tests inject fakes and never reach a network,
// a real database file or the real cookie store.
type Deps struct {
	// NewProvider builds the order source from the config.
	NewProvider func(cfg *config.Config, log *slog.Logger) (order.OrderProvider, error)
	// NewChat builds the LLM client from the environment.
	NewChat func(getenv func(string) string) (categorizer.ChatClient, error)
	// NewStore opens the database at path.
	NewStore func(ctx context.Context, path string) (Store, error)
	// Now is the clock.
	Now func() time.Time
	// Sleep waits d or until ctx is done; nil means sync.SleepContext.
	Sleep func(ctx context.Context, d time.Duration) error
}
