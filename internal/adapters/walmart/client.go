package walmart

import (
	"context"
	"log/slog"
	"time"

	wm "github.com/eshaffer321/walmart-client-go/v2"
)

// Client is the narrow slice of *wm.WalmartClient this adapter calls. Tests
// use a fake; production uses the real client from NewClient.
type Client interface {
	GetPurchaseHistory(ctx context.Context, req wm.PurchaseHistoryRequest) (*wm.PurchaseHistoryResponse, error)
	GetOrderWithGroup(ctx context.Context, orderID, groupID string, isInStore bool) (*wm.Order, error)
	GetOrderLedger(ctx context.Context, orderID string) (*wm.OrderLedger, error)
}

var _ Client = (*wm.WalmartClient)(nil)

// ClientOption tunes the real client built by NewClient.
type ClientOption func(*wm.ClientConfig)

// WithMaxRetries sets the client's ledger 429 retry count. 0 keeps the client
// default of 3 retries (5s, 10s, 20s backoff); -1 disables retries.
func WithMaxRetries(n int) ClientOption {
	return func(c *wm.ClientConfig) { c.MaxRetries = n }
}

// WithRateLimits sets the client's own spacing between history/order calls and
// between ledger calls. Zero values keep the client defaults.
func WithRateLimits(general, ledger time.Duration) ClientOption {
	return func(c *wm.ClientConfig) {
		c.RateLimit = general
		c.LedgerRateLimit = ledger
	}
}

// WithClientLogger gives the real client a logger. The client logs order IDs
// and file paths, never cookie values.
func WithClientLogger(l *slog.Logger) ClientOption {
	return func(c *wm.ClientConfig) { c.Logger = l }
}

func newClientConfig(cookieFile string, opts ...ClientOption) wm.ClientConfig {
	cfg := wm.ClientConfig{CookieFile: cookieFile}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// NewClient builds the real walmart-client-go client reading cookies from
// cookieFile (empty means the client's default ~/.walmart-api/cookies.json).
// It makes no network call; a missing cookie file yields an empty store, so
// callers should check CookieCount before use.
func NewClient(cookieFile string, opts ...ClientOption) (*wm.WalmartClient, error) {
	return wm.NewWalmartClient(newClientConfig(cookieFile, opts...))
}
