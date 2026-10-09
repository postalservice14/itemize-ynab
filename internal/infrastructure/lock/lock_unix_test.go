//go:build unix && !aix && !solaris

package lock_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/infrastructure/lock"
)

func TestAcquire_secondAcquireFailsWithoutWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iy.db.lock")
	release, err := lock.Acquire(path)
	require.NoError(t, err)
	t.Cleanup(release)

	again, err := lock.Acquire(path)

	require.ErrorIs(t, err, lock.ErrLocked)
	assert.Nil(t, again)
	assert.Contains(t, err.Error(), path)
}

func TestAcquire_releaseThenReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iy.db.lock")
	release, err := lock.Acquire(path)
	require.NoError(t, err)
	release()
	release() // releasing twice is harmless

	again, err := lock.Acquire(path)

	require.NoError(t, err)
	again()
}

func TestAcquire_createsAPrivateLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iy.db.lock")
	release, err := lock.Acquire(path)
	require.NoError(t, err)
	defer release()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestAcquire_unopenablePath_errorIsNotErrLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "iy.db.lock")

	release, err := lock.Acquire(path)

	require.Error(t, err)
	assert.Nil(t, release)
	assert.False(t, errors.Is(err, lock.ErrLocked))
	assert.Contains(t, err.Error(), path)
}
