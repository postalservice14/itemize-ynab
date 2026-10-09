package walmart

import (
	"context"
	"errors"
	"fmt"
	"strings"

	wm "github.com/eshaffer321/walmart-client-go/v2"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

// maxErrText bounds how much of an unclassified client error is carried into
// our errors. Order and history failures read "HTTP N: <body>" and the body
// can be large and hold account data.
const maxErrText = 200

// classifyError reports whether err means Walmart blocked the session, and why.
// It is the single place that decides this.
//
// Only HTTP 456 is typed in the client (wm.ErrBotChallenge). The 403/418 and
// 429 outcomes are untyped strings, so they are matched by substring. The
// strings below were verified against walmart-client-go v2.2.1 (orders.go,
// purchases.go and ledger.go); re-verify them whenever the client is upgraded:
//
//	403/418: "access denied - cookies expired, ..." / "access denied (cookies might be stale) ..."
//	429:     "rate limited - cookies might be stale, ..." / "after N retries: rate limited (attempt a/b)"
//
// Errors that embed a response body ("HTTP N: <body>") are never string
// matched, so a body that happens to say "rate limited" cannot stop a run.
func classifyError(err error) (order.BlockedKind, bool) {
	if err == nil {
		return 0, false
	}
	if errors.Is(err, wm.ErrBotChallenge) {
		return order.BotChallenge, true
	}
	msg := err.Error()
	if strings.Contains(msg, "HTTP ") {
		return 0, false
	}
	switch {
	case strings.Contains(msg, "access denied"):
		return order.StaleSession, true
	case strings.Contains(msg, "rate limited"):
		return order.RateLimited, true
	}
	return 0, false
}

// wrapError turns a client error into our error. A blocked session becomes an
// *order.BlockedError (stop the run); anything else is a per-call failure
// carrying the order ID and a bounded message. Cookies and headers never reach
// the message: the client's error texts do not contain them.
func wrapError(orderID, op string, err error) error {
	if kind, ok := classifyError(err); ok {
		return &order.BlockedError{Provider: ProviderName, Kind: kind, Err: fmt.Errorf("order %s: %s: %w", orderID, op, err)}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("walmart: order %s: %s: %w", orderID, op, err)
	}
	return fmt.Errorf("walmart: order %s: %s: %s", orderID, op, truncate(err.Error(), maxErrText))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
