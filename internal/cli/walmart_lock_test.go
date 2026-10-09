//go:build unix && !aix && !solaris

package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/infrastructure/lock"
)

// holdRunLock takes the run lock next to the harness database, as an
// overlapping run would.
func (h *wmHarness) holdRunLock() string {
	h.t.Helper()
	path := filepath.Join(h.dir, "iy.db.lock")
	release, err := lock.Acquire(path)
	require.NoError(h.t, err)
	h.t.Cleanup(release)
	return path
}

func TestWalmart_lockHeld_exitsOneBeforeAnyCall(t *testing.T) {
	for name, args := range map[string][]string{
		"real run": {"walmart", "-days", "7"},
		"dry run":  {"walmart", "-dry-run", "-days", "7"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newWMHarness(t)
			h.script()
			path := h.holdRunLock()

			r := h.run(args...)

			assert.Equal(t, 1, r.code)
			assert.Contains(t, r.stderr, "another itemize-ynab run is in progress (lock: "+path+")")
			assert.Empty(t, r.stdout)
			assert.Zero(t, h.provFactory+h.chatFactory, "nothing is even built")
			h.untouched()
		})
	}
}

func TestWalmart_runTakesAndReleasesTheLockNextToTheDatabase(t *testing.T) {
	h := newWMHarness(t)
	h.script()

	r := h.run("walmart", "-days", "7")

	require.Equal(t, 0, r.code, r.stderr)
	path := filepath.Join(h.dir, "iy.db.lock")
	_, err := os.Stat(path)
	require.NoError(t, err, "the lock file lives next to the database")
	release, err := lock.Acquire(path)
	require.NoError(t, err, "the lock is released when the run ends")
	release()
}

func TestWalmart_lockHeld_utilityCommandsStillRun(t *testing.T) {
	h := newWMHarness(t)
	h.holdRunLock()
	capture := filepath.Join(h.dir, "capture.txt")
	require.NoError(t, os.WriteFile(capture, []byte(syntheticCurl()), 0o600))

	accounts := h.run("ynab", "accounts")
	imported := h.run("walmart", "import-curl", capture)

	assert.Equal(t, 0, accounts.code, accounts.stderr)
	assert.Equal(t, 0, imported.code, imported.stderr)
}
