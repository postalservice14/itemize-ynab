package order

import (
	"context"
	"time"
)

// OrderRef identifies an order returned by listing. Token is opaque provider
// data (for example the group and fulfillment hints needed to fetch detail);
// consumers pass the ref back unchanged and never interpret it.
//
//nolint:revive // the port names are fixed by the task contract
type OrderRef struct {
	ID    string
	Token string
}

// OrderProvider is the narrow, retailer-agnostic seam to an order source.
//
// Implementations return an error satisfying errors.Is(err, ErrBlocked) when
// the retailer blocks the session; that condition must stop the whole run. Any
// other error is specific to the call and the caller may continue with the
// next order. Implementations do not pace themselves between calls; the
// orchestrator owns inter-request delays.
//
//nolint:revive // the port names are fixed by the task contract
type OrderProvider interface {
	// Orders lists the orders placed on or after since. Ordering is the
	// provider's (newest first for Walmart).
	Orders(ctx context.Context, since time.Time) ([]OrderRef, error)
	// Order fetches the full order: items, totals and card charges.
	Order(ctx context.Context, ref OrderRef) (Order, error)
}
