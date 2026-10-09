package cli_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real orchestrator, YNAB client, writer, categorizer and SQLite store run
// behind cli.Run; only Walmart, the LLM and the YNAB server are fakes.
func TestWalmart_secondRunIsIdempotent(t *testing.T) {
	h := newWMHarness(t)
	h.script()

	first := h.run("walmart", "-days", "7")

	require.Equal(t, 0, first.code, first.stderr)
	t.Logf("first run stdout:\n%s", first.stdout)
	assert.Equal(t, 2, h.srv.WriteCount())
	assert.Equal(t, 2, h.chat.calls)
	assert.Len(t, h.srv.Calls(http.MethodPut, planPath+"/transactions/tA"), 1)
	assert.Len(t, h.srv.Calls(http.MethodPut, planPath+"/transactions/tB"), 1)

	h.srv.Reset()
	h.chat.calls = 0
	second := h.run("walmart", "-days", "7")

	require.Equal(t, 0, second.code, second.stderr)
	t.Logf("second run stdout:\n%s", second.stdout)
	assert.Equal(t, 0, h.srv.WriteCount(), "the second run must not write to YNAB")
	assert.Zero(t, h.chat.calls, "already processed charges cost no LLM call")
	assert.Contains(t, second.stdout, "Totals: 2 already_processed (2 total)")
	assert.NotContains(t, second.stdout, "categorized  ")
	assert.Contains(t, second.stdout, "recorded as categorized")
	assert.Contains(t, second.stdout, "recorded as split_in_place")
}

// The fake YNAB server keeps listing the original, unmarked transactions (as
// if the markers had been removed by hand), so only the store record stands
// between a rerun and a second write.
func TestWalmart_forceRerunWritesAgainOnlyWhenAsked(t *testing.T) {
	h := newWMHarness(t)
	h.script()
	require.Equal(t, 0, h.run("walmart", "-days", "7").code)
	h.srv.Reset()

	plain := h.run("walmart", "-days", "7")
	require.Equal(t, 0, plain.code, plain.stderr)
	assert.Equal(t, 0, h.srv.WriteCount(), "without -force the record prevents a write")

	dry := h.run("walmart", "-days", "7", "-force", "-dry-run")
	require.Equal(t, 0, dry.code, dry.stderr)
	assert.NotContains(t, dry.stdout, "already_processed")
	assert.Equal(t, 0, h.srv.WriteCount(), "a forced dry run plans but writes nothing")

	forced := h.run("walmart", "-days", "7", "-force")
	require.Equal(t, 0, forced.code, forced.stderr)
	assert.NotContains(t, forced.stdout, "already_processed")
	assert.Equal(t, 2, h.srv.WriteCount(), "a real forced rerun writes both charges again")
}
