//go:build !unix || aix || solaris

package lock

// Acquire has no run lock to take on this platform. It returns a no-op
// release (safe to call) and ErrUnsupported, so the caller refuses to run
// rather than risk two overlapping runs writing duplicates.
func Acquire(string) (func(), error) { return func() {}, ErrUnsupported }
