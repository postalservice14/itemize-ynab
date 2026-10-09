package order

import (
	"errors"
	"fmt"
)

// ErrBlocked matches (via errors.Is) any BlockedError: the retailer refused or
// throttled the session. The run must stop; the CLI maps it to exit code 3.
var ErrBlocked = errors.New("order: retailer blocked the session")

// BlockedKind says why the retailer stopped serving requests.
type BlockedKind int

// Blocked kinds.
const (
	// BotChallenge: the retailer answered with an anti-automation challenge.
	BotChallenge BlockedKind = iota + 1
	// StaleSession: cookies or the captured request profile were rejected.
	StaleSession
	// RateLimited: the retailer throttled us.
	RateLimited
)

// String returns a short human-readable label.
func (k BlockedKind) String() string {
	switch k {
	case BotChallenge:
		return "bot challenge"
	case StaleSession:
		return "stale session"
	case RateLimited:
		return "rate limited"
	default:
		return fmt.Sprintf("unknown(%d)", int(k))
	}
}

// BlockedError reports a blocked session. Err is the underlying client error;
// it must not carry cookies or headers (the adapter only wraps errors whose
// text it has verified to be free of them).
type BlockedError struct {
	Provider string
	Kind     BlockedKind
	Err      error
}

// Error implements error.
func (e *BlockedError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s: blocked (%s)", e.Provider, e.Kind)
	}
	return fmt.Sprintf("%s: blocked (%s): %v", e.Provider, e.Kind, e.Err)
}

// Unwrap exposes the underlying error.
func (e *BlockedError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrBlocked) true for any BlockedError.
func (e *BlockedError) Is(target error) bool { return target == ErrBlocked }
