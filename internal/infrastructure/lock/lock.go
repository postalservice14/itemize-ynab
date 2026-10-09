// Package lock is the run lock: an exclusive, non-blocking advisory lock on a
// file, so two runs never write to the same YNAB plan at the same time. The
// kernel releases the lock when the process exits, even after a crash, so
// there is never a stale lock to clean up.
package lock

import "errors"

// ErrLocked means another process (or another Acquire in this one) holds the
// lock. Acquire never waits for it.
var ErrLocked = errors.New("lock: already held")

// ErrUnsupported means this platform has no run lock.
var ErrUnsupported = errors.New("lock: the run lock is not supported on this platform")
