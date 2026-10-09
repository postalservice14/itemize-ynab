package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

func TestRunOrchestrator_walmartBlockedOnSecondOrder_partialAndExit3(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()
	h.prov.add(order.Order{ID: "oC", DisplayID: "C-300"})
	h.prov.errs["oB"] = &order.BlockedError{Provider: "walmart", Kind: order.BotChallenge, Err: errors.New("HTTP 456")}

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusCategorized}, statuses(sum.Rows), "first order's rows intact")
	assert.Equal(t, "tA", sum.Rows[0].TxnID)
	require.NotNil(t, sum.StoppedEarly)
	assert.Equal(t, StopWalmartBlocked, sum.StoppedEarly.Source)
	assert.Equal(t, StopKindWalmartBotChallenge, sum.StoppedEarly.Kind)
	assert.Contains(t, sum.StoppedEarly.Reason, "bot challenge")
	assert.Equal(t, 3, sum.ExitCode())
	assert.Equal(t, []string{"oA", "oB"}, h.prov.fetched, "no further fetches")
}

func TestRunOrchestrator_walmartBlockedOnListing_exit3NoRows(t *testing.T) {
	h := newOrchHarness(t)
	h.prov.listErr = fmt.Errorf("list: %w", &order.BlockedError{Provider: "walmart", Kind: order.StaleSession})

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	assert.Empty(t, sum.Rows)
	require.NotNil(t, sum.StoppedEarly)
	assert.Contains(t, sum.StoppedEarly.Reason, "stale session")
	assert.Equal(t, 3, sum.ExitCode())
}

func TestRunOrchestrator_walmartBlocked_kindFollowsTheBlock(t *testing.T) {
	for kind, want := range map[order.BlockedKind]StopKind{
		order.BotChallenge:    StopKindWalmartBotChallenge,
		order.StaleSession:    StopKindWalmartStaleSession,
		order.RateLimited:     StopKindWalmartRateLimited,
		order.BlockedKind(99): StopKindOther,
	} {
		t.Run(kind.String(), func(t *testing.T) {
			h := newOrchHarness(t)
			h.prov.listErr = fmt.Errorf("list: %w", &order.BlockedError{Provider: "walmart", Kind: kind})

			sum, err := h.run(t, Options{Days: 14})

			require.NoError(t, err)
			require.NotNil(t, sum.StoppedEarly)
			assert.Equal(t, want, sum.StoppedEarly.Kind)
		})
	}
}

func TestRunOrchestrator_walmartBlockedWithoutBlockedError_kindOther(t *testing.T) {
	h := newOrchHarness(t)
	h.prov.listErr = fmt.Errorf("list: %w", order.ErrBlocked)

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.NotNil(t, sum.StoppedEarly)
	assert.Equal(t, StopKindOther, sum.StoppedEarly.Kind)
	assert.Equal(t, "Walmart blocked", sum.StoppedEarly.Reason)
}

func TestRunOrchestrator_walmartListingFails_returnsError(t *testing.T) {
	h := newOrchHarness(t)
	h.prov.listErr = errors.New("walmart: purchase history: HTTP 500")

	_, err := h.run(t, Options{Days: 14})

	require.Error(t, err)
	assert.Equal(t, 1, ExitCodeForError(err))
}

func TestRunOrchestrator_ynab429MidRun_partialExit3EarlierRecorded(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()
	h.prov.add(order.Order{ID: "oC", DisplayID: "C-300"})
	h.srv.On(http.MethodPut, txnPath("tB1"), ynabtest.RateLimited())

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusCategorized}, statuses(sum.Rows))
	require.NotNil(t, sum.StoppedEarly)
	assert.Equal(t, StopYNABRateLimit, sum.StoppedEarly.Source)
	assert.Equal(t, StopKindYNABRateLimit, sum.StoppedEarly.Kind)
	assert.Equal(t, "YNAB rate limit", sum.StoppedEarly.Reason)
	assert.Equal(t, 3, sum.ExitCode())
	_, recordedA := h.recorded(t, "walmart:oA:500:1")
	assert.True(t, recordedA, "the earlier charge is recorded")
	assert.Equal(t, 2, h.srv.WriteCount(), "no write after the 429")
	assert.Empty(t, h.srv.Calls(http.MethodPut, txnPath("tB2")))
	assert.Equal(t, []string{"oA", "oB"}, h.prov.fetched, "no further fetches")
}

func TestRunOrchestrator_ynab429OnTransactionsList_stops(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()
	h.srv.On(http.MethodGet, txnListPath, ynabtest.RateLimited())

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	assert.Empty(t, sum.Rows)
	require.NotNil(t, sum.StoppedEarly)
	assert.Equal(t, StopYNABRateLimit, sum.StoppedEarly.Source)
	assert.Equal(t, []string{"oA"}, h.prov.fetched)
}

func TestRunOrchestrator_llmFailure_failsThatOrderOnly(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()
	h.cat.errFor = map[string]error{"Milk": fmt.Errorf("categorizer: LLM call failed: %w", categorizer.ErrRateLimited)}

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusFailed, StatusSplitInPlace, StatusSplitInPlace}, statuses(sum.Rows))
	assert.Contains(t, sum.Rows[0].Err, "rate limited")
	assert.Equal(t, int64(500), sum.Rows[0].AmountCents)
	assert.Nil(t, sum.StoppedEarly, "an LLM rate limit is not run-stopping")
	assert.Equal(t, 2, sum.ExitCode())
	assert.Empty(t, h.srv.Calls(http.MethodPut, txnPath("tA")))
}

// An allowed name with no single YNAB ID (it exists in two groups) reaches
// the splitter unmapped: the charge fails, naming it, with no default.
func TestRunOrchestrator_unmappedCategory_namedFailureNoDefault(t *testing.T) {
	h := newOrchHarness(t)
	h.srv.On(http.MethodGet, categoriesPath, ynabtest.OK(ynabtest.CategoriesData(1,
		ynabtest.Group{ID: "g-food", Name: "Food", Categories: []ynabtest.Cat{{ID: catGroc, Name: "Groceries"}}},
		ynabtest.Group{ID: "g-fam", Name: "Family", Categories: []ynabtest.Cat{{ID: "cat-g1", Name: "Gifts"}}},
		ynabtest.Group{ID: "g-hol", Name: "Holidays", Categories: []ynabtest.Cat{{ID: "cat-g2", Name: "Gifts"}}},
	)))
	h.cat.byItem["Widget"] = "Gifts"
	o := order.Order{ID: "oU", DisplayID: "U-1", Items: []order.Item{item("Milk", 100), item("Widget", 100)},
		Charges: []order.Charge{charge("oU", 200, 1, date(10, 5))}}
	h.prov.add(o)
	h.withTxns(wm("tU", acctA, -2000, date(10, 5)))

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusFailed}, statuses(sum.Rows))
	assert.Contains(t, sum.Rows[0].Err, `"Gifts"`)
	assert.Contains(t, sum.Rows[0].Err, "Family")
	assert.Contains(t, sum.Rows[0].Err, "Holidays")
	assert.Contains(t, h.cat.allowed[0], "Gifts", "the name was offered, so the model's answer was valid")
	assert.Equal(t, 0, h.srv.WriteCount(), "nothing written, no default category")
	assert.Equal(t, 1, h.txnListCalls(), "transactions load before categorizing; no charge reached the writer")
	assert.Equal(t, 2, sum.ExitCode())
}

func TestRunOrchestrator_giftCardRefundAndEmptyOrders_skippedRows(t *testing.T) {
	h := newOrchHarness(t)
	h.prov.add(
		order.Order{ID: "oG", DisplayID: "G-1", Items: []order.Item{item("Milk", 1000)},
			Skipped: []order.SkippedCharge{{AmountCents: 1000, Reason: order.GiftCard, PaymentType: "GIFTCARD"}}},
		order.Order{ID: "oR", DisplayID: "R-1", Items: []order.Item{item("Milk", 500)},
			Skipped: []order.SkippedCharge{{AmountCents: -500, Reason: order.Refund}}},
		order.Order{ID: "oE", DisplayID: "E-1"},
	)

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusSkipped, StatusSkipped, StatusSkipped}, statuses(sum.Rows))
	assert.Equal(t, Row{OrderDisplayID: "G-1", AmountCents: 1000, Status: StatusSkipped, Note: "gift card"}, sum.Rows[0])
	assert.Equal(t, Row{OrderDisplayID: "R-1", AmountCents: -500, Status: StatusSkipped, Note: "refund"}, sum.Rows[1])
	assert.Equal(t, "E-1", sum.Rows[2].OrderDisplayID)
	assert.Contains(t, sum.Rows[2].Note, "no card charges")
	assert.Empty(t, h.cat.calls)
	assert.Equal(t, 0, h.srv.WriteCount())
	assert.Equal(t, 0, h.txnListCalls())
	assert.Equal(t, 0, sum.ExitCode())
}

func TestRunOrchestrator_configCrossCheckFails_noWalmartNoWrites(t *testing.T) {
	cases := map[string]func(*config.YNAB){
		"unknown account": func(y *config.YNAB) { y.Accounts = map[string]string{cardA: "acct-missing"} },
		"bad override":    func(y *config.YNAB) { y.Accounts = nil; y.CategoryOverrides = map[string]string{"Pets": "Nope"} },
		"bad split mode":  func(y *config.YNAB) { y.SplitInPlace = "sometimes" },
		"no flag color":   func(y *config.YNAB) { y.FlagColor = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newOrchHarness(t)
			h.happy()
			mutate(&h.cfg.YNAB)

			_, err := h.run(t, Options{Days: 14})

			require.ErrorIs(t, err, config.ErrInvalid)
			assert.Equal(t, 1, ExitCodeForError(err))
			assert.Equal(t, 0, h.prov.calls(), "zero Walmart calls")
			assert.Equal(t, 0, h.srv.WriteCount(), "zero writes")
			assert.Empty(t, h.cat.calls)
		})
	}
}

func TestRunOrchestrator_ynabErrorsBeforeWalmart(t *testing.T) {
	cases := map[string]struct {
		resp ynabtest.Response
		path string
		want int
		is   error
	}{
		"401 on categories": {ynabtest.APIError(http.StatusUnauthorized, "401", "unauthorized", "bad token"), categoriesPath, 1, ynab.ErrUnauthorized},
		"403 on accounts":   {ynabtest.APIError(http.StatusForbidden, "403", "forbidden", "no access"), accountsPath, 1, ynab.ErrUnauthorized},
		"429 on categories": {ynabtest.RateLimited(), categoriesPath, 3, ynab.ErrRateLimited},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newOrchHarness(t)
			h.happy()
			h.srv.On(http.MethodGet, tc.path, tc.resp)

			_, err := h.run(t, Options{Days: 14})

			require.ErrorIs(t, err, tc.is)
			assert.Equal(t, tc.want, ExitCodeForError(err))
			assert.Equal(t, 0, h.prov.calls(), "zero Walmart calls")
			assert.NotContains(t, err.Error(), testToken)
		})
	}
}

func TestRunOrchestrator_orderFetchError_failedRowRunContinues(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()
	h.prov.errs["oA"] = errors.New("walmart: order oA: getOrder: HTTP 500")

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusFailed, StatusSplitInPlace, StatusSplitInPlace}, statuses(sum.Rows))
	assert.Equal(t, "oA", sum.Rows[0].OrderDisplayID)
	assert.Contains(t, sum.Rows[0].Err, "HTTP 500")
	assert.Equal(t, 2, sum.ExitCode())
}

func TestRunOrchestrator_transactionsLoadFails_chargesFailOneAttempt(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()
	h.srv.On(http.MethodGet, txnListPath, serverError())

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusFailed, StatusFailed, StatusFailed}, statuses(sum.Rows))
	assert.Contains(t, sum.Rows[2].Err, "HTTP 500")
	assert.Equal(t, 1, h.txnListCalls(), "the failed load is not retried per charge")
	assert.Empty(t, h.cat.calls, "no LLM tokens are spent once the transactions cannot be loaded")
	assert.Equal(t, 0, h.srv.WriteCount())
	assert.Equal(t, 2, sum.ExitCode())
}

func TestRunOrchestrator_writerError_failedRowRunContinues(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()
	h.srv.On(http.MethodPut, txnPath("tA"), serverError())

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusFailed, StatusSplitInPlace, StatusSplitInPlace}, statuses(sum.Rows))
	assert.Contains(t, sum.Rows[0].Err, "HTTP 500")
	assert.Len(t, sum.Rows[0].Splits, 1, "the attempted split is still shown")
	assert.Equal(t, 2, sum.ExitCode())
}

func TestRunOrchestrator_canceled_stopsCleanlyWithPartialSummary(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()
	h.sleepErr = context.Canceled

	sum, err := h.run(t, Options{Days: 14})

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []Status{StatusCategorized}, statuses(sum.Rows))
	assert.Equal(t, []string{"oA"}, h.prov.fetched)
	assert.Equal(t, 1, ExitCodeForError(err))
}

func TestRunOrchestrator_invalidOptions(t *testing.T) {
	h := newOrchHarness(t)
	for _, opts := range []Options{{Days: -1}, {Days: 14, Max: -1}} {
		_, err := h.run(t, opts)
		require.Error(t, err, "%+v", opts)
		assert.Equal(t, 1, ExitCodeForError(err))
	}
	assert.Equal(t, 0, h.prov.calls())
}

func TestNewOrchestrator_requiresDependencies(t *testing.T) {
	h := newOrchHarness(t)
	full := Deps{Provider: h.prov, Categorizer: h.cat, YNAB: h.api, Writes: h.api,
		Transactions: NewTransactionSource(h.api, h.store, quiet), Store: h.store, Now: func() time.Time { return now }}
	_, err := NewOrchestrator(full)
	require.NoError(t, err, "Sleep and Logger default")

	noClock := full
	noClock.Now = nil
	_, err = NewOrchestrator(noClock)
	require.Error(t, err)

	noProvider := full
	noProvider.Provider = nil
	_, err = NewOrchestrator(noProvider)
	require.Error(t, err)
}
