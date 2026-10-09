//go:build unix && !aix && !solaris

package lock

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
)

// Acquire takes an exclusive flock(2) on path, creating the file (0600) if
// needed. It never blocks: when the lock is held it returns an error matching
// ErrLocked. The returned release unlocks and closes the file; it is safe to
// call more than once. The lock file itself is left in place.
func Acquire(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // the path comes from the user's own config
	if err != nil {
		return nil, fmt.Errorf("lock: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return nil, fmt.Errorf("lock: flock %s: %w", path, err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		})
	}, nil
}
