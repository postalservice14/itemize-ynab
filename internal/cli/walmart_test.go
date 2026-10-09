package cli_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

func TestWalmart_allOKExitsZeroAndPrintsSummary(t *testing.T) {
	h := newWMHarness(t)
	h.script()

	r := h.run("walmart", "-days", "7")

	require.Equal(t, 0, r.code, r.stderr)
	for _, want := range []string{"ORDER", "A-100", "$5.00", "categorized", "B-200", "$100.00", "split_in_place",
		"Totals: 1 categorized, 1 split_in_place (2 total)"} {
		assert.Contains(t, r.stdout, want)
	}
	assert.NotContains(t, r.stdout, "DRY RUN")
	assert.NotContains(t, r.stdout, "level=", "logs go to stderr, not stdout")
	assert.Equal(t, 2, h.srv.WriteCount())
}

func TestWalmart_failedRowExitsTwo(t *testing.T) {
	h := newWMHarness(t)
	h.script()
	h.prov.errs["oB"] = errors.New("walmart: order oB: get order: boom")

	r := h.run("walmart", "-days", "7")

	assert.Equal(t, 2, r.code)
	assert.Contains(t, r.stdout, "failed")
	assert.Contains(t, r.stdout, "boom")
	assert.Contains(t, r.stderr, "1 charge(s) failed")
	assert.Equal(t, 1, h.srv.WriteCount(), "the good order is still written")
}

func TestWalmart_skippedOnlyIsStillExitZero(t *testing.T) {
	h := newWMHarness(t)
	h.prov.add(order.Order{ID: "oG", DisplayID: "G-900", Date: day(10, 5), Skipped: []order.SkippedCharge{
		{AmountCents: 2500, Reason: order.GiftCard, PaymentType: "GIFTCARD"}}})

	r := h.run("walmart", "-days", "7")

	assert.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "skipped")
	assert.Contains(t, r.stdout, "gift card")
	assert.Equal(t, 0, h.srv.WriteCount())
}

func TestWalmart_stoppedEarlyExitsThree(t *testing.T) {
	t.Run("YNAB rate limit during a write", func(t *testing.T) {
		h := newWMHarness(t)
		h.script()
		h.srv.On(http.MethodPut, planPath+"/transactions/tA", ynabtest.RateLimited())

		r := h.run("walmart", "-days", "7")

		assert.Equal(t, 3, r.code)
		assert.Contains(t, r.stdout, "STOPPED EARLY: YNAB rate limit")
		assert.Contains(t, r.stdout, "200 requests/hour per token")
		assert.Contains(t, r.stderr, "stopped early")
	})
	t.Run("YNAB rate limit in the preflight", func(t *testing.T) {
		h := newWMHarness(t)
		h.script()
		h.srv.On(http.MethodGet, planPath+"/categories", ynabtest.RateLimited())

		r := h.run("walmart", "-days", "7")

		assert.Equal(t, 3, r.code)
		assert.Contains(t, r.stderr, "rate limit")
		assert.Zero(t, h.prov.calls, "no Walmart call after a failed preflight")
	})
	t.Run("Walmart bot challenge", func(t *testing.T) {
		h := newWMHarness(t)
		h.script()
		h.prov.errs["oA"] = &order.BlockedError{Provider: "walmart", Kind: order.BotChallenge, Err: errors.New("HTTP 456")}

		r := h.run("walmart", "-days", "7")

		assert.Equal(t, 3, r.code)
		assert.Contains(t, r.stdout, "STOPPED EARLY: Walmart blocked: bot challenge")
		assert.Contains(t, r.stdout, "refresh your Walmart cookies")
		assert.Equal(t, 0, h.srv.WriteCount())
	})
}

func TestWalmart_stoppedEarlyBeatsFailedRows(t *testing.T) {
	h := newWMHarness(t)
	h.script()
	h.prov.errs["oA"] = errors.New("walmart: order oA: boom")
	h.prov.errs["oB"] = &order.BlockedError{Provider: "walmart", Kind: order.StaleSession}

	r := h.run("walmart", "-days", "7")

	assert.Equal(t, 3, r.code, "3 takes priority over 2")
	assert.Contains(t, r.stdout, "failed")
	assert.Contains(t, r.stdout, "STOPPED EARLY")
}

func TestWalmart_authFailureIsExitOne(t *testing.T) {
	h := newWMHarness(t)
	h.script()
	h.srv.On(http.MethodGet, planPath+"/categories", ynabtest.APIError(http.StatusUnauthorized, "401", "unauthorized", "bad token"))

	r := h.run("walmart", "-days", "7")

	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "error:")
	assert.Zero(t, h.prov.calls)
}

func TestWalmart_cancelledRunPrintsPartialSummaryThenErrorExitsOne(t *testing.T) {
	h := newWMHarness(t)
	h.script()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.onSleep = cancel // Ctrl-C arrives while waiting between the two order fetches

	r := h.runCtx(ctx, "walmart", "-days", "7")

	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stdout, "A-100", "the finished order is in the partial summary")
	assert.NotContains(t, r.stdout, "B-200")
	assert.Contains(t, r.stderr, "interrupted")
	assert.Equal(t, 1, h.srv.WriteCount())
}

func TestWalmart_configAndSetupErrorsExitOneBeforeAnyCall(t *testing.T) {
	tests := map[string]func(h *wmHarness){
		"missing config file": func(h *wmHarness) { h.cfg = filepath.Join(h.dir, "absent.yaml") },
		"missing YNAB token env": func(*wmHarness) {
			t.Setenv("YNAB_TOKEN", "")
		},
		"invalid config value": func(h *wmHarness) { h.writeConfig("  split_in_place: sometimes\n") },
		"missing LLM key": func(h *wmHarness) {
			h.useDefaultChat = true
		},
		"unopenable database": func(h *wmHarness) {
			blocker := filepath.Join(h.dir, "afile")
			require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
			h.writeConfig("")
			cfg, err := os.ReadFile(h.cfg)
			require.NoError(t, err)
			bad := strings.ReplaceAll(string(cfg), filepath.Join(h.dir, "iy.db"), filepath.Join(blocker, "sub", "iy.db"))
			require.NoError(t, os.WriteFile(h.cfg, []byte(bad), 0o600)) //nolint:gosec // a path inside t.TempDir()
		},
		"unusable cookie file": func(h *wmHarness) { h.useDefaultProvider = true },
	}
	wantMentions := map[string]string{
		"missing config file":    "absent.yaml",
		"missing YNAB token env": "YNAB_TOKEN",
		"invalid config value":   "split_in_place",
		"missing LLM key":        "ANTHROPIC_API_KEY",
		"unopenable database":    "database.path",
		"unusable cookie file":   "walmart.cookie_file",
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			h := newWMHarness(t)
			h.script()
			mutate(h)

			r := h.run("walmart", "-days", "7")

			assert.Equal(t, 1, r.code, r.stderr)
			assert.Contains(t, r.stderr, wantMentions[name])
			assert.Empty(t, r.stdout, "no summary when setup failed")
			assert.Empty(t, h.srv.Requests(), "no YNAB request before setup is complete")
			assert.Zero(t, h.prov.calls)
			assert.Zero(t, h.chat.calls)
		})
	}
}

func TestWalmart_missingLLMKeyStopsBeforeProviderFactory(t *testing.T) {
	h := newWMHarness(t)
	h.useDefaultChat = true

	r := h.run("walmart")

	assert.Equal(t, 1, r.code)
	assert.Zero(t, h.provFactory, "the cookie store is not even opened without an LLM key")
	h.untouched()
}

func TestWalmart_usageErrorsExitOneWithUsageAndNoSetup(t *testing.T) {
	for name, args := range map[string][]string{
		"zero days":      {"walmart", "-days", "0"},
		"negative max":   {"walmart", "-max", "-2"},
		"unknown flag":   {"walmart", "-bogus"},
		"stray argument": {"walmart", "oops"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newWMHarness(t)

			r := h.run(args...)

			assert.Equal(t, 1, r.code)
			assert.Contains(t, r.stderr, "usage:")
			assert.Zero(t, h.provFactory+h.chatFactory)
			h.untouched()
		})
	}
}

func TestWalmart_dryRunWritesNothingAndSaysSo(t *testing.T) {
	h := newWMHarness(t)
	h.script()

	r := h.run("walmart", "-dry-run", "-days", "7")

	require.Equal(t, 0, r.code, r.stderr)
	assert.True(t, strings.HasPrefix(r.stdout, "DRY RUN — no YNAB writes were made\n"), r.stdout)
	assert.Contains(t, r.stdout, "categorized (planned)")
	assert.Contains(t, r.stdout, "Totals (DRY RUN):")
	assert.Equal(t, 0, h.srv.WriteCount())
}

func TestWalmart_verboseFlagControlsLogLevelAndSplitListing(t *testing.T) {
	quiet := newWMHarness(t)
	quiet.script()
	loud := newWMHarness(t)
	loud.script()

	q := quiet.run("walmart", "-days", "7")
	l := loud.run("walmart", "-days", "7", "-verbose")

	require.Equal(t, 0, q.code, q.stderr)
	require.Equal(t, 0, l.code, l.stderr)
	assert.Contains(t, q.stderr, "level=INFO")
	assert.NotContains(t, q.stderr, "level=DEBUG")
	assert.Contains(t, l.stderr, "level=DEBUG")
	assert.NotContains(t, q.stdout, "→")
	assert.Contains(t, l.stdout, "Household → $60.00")
	assert.Contains(t, l.stdout, "Groceries → $40.00")
}

func TestWalmart_maxLimitsOrdersProcessed(t *testing.T) {
	h := newWMHarness(t)
	h.script()

	r := h.run("walmart", "-days", "7", "-max", "1")

	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "A-100")
	assert.NotContains(t, r.stdout, "B-200")
}

func TestWalmart_daysFlagReachesTheProviderSince(t *testing.T) {
	h := newWMHarness(t)
	h.srv.On(http.MethodGet, planPath+"/transactions", ynabtest.OK(ynabtest.TransactionsData(10)))

	r := h.run("walmart", "-days", "7")

	assert.Equal(t, 0, r.code, r.stderr)
	assert.Equal(t, 1, h.prov.calls, "one listing call, no orders")
	assert.Contains(t, r.stdout, "Nothing to do")
}
