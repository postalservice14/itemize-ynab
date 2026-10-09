package sync

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/storage"
)

func TestRunOrchestrator_happyPath_rowsCallsSplitsAndPacing(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusCategorized, StatusSplitInPlace, StatusSplitInPlace}, statuses(sum.Rows))
	assert.Equal(t, Row{
		OrderDisplayID: "A-100", AmountCents: 500, Status: StatusCategorized, TxnID: "tA",
		Splits: []SplitView{{Category: "Groceries", CategoryID: catGroc, AmountCents: 500}},
	}, sum.Rows[0])
	assert.Equal(t, []SplitView{
		{Category: "Household", CategoryID: catHome, AmountCents: 6300},
		{Category: "Groceries", CategoryID: catGroc, AmountCents: 1050},
	}, sum.Rows[1].Splits)
	assert.Equal(t, "B-200", sum.Rows[2].OrderDisplayID)
	assert.Equal(t, int64(3150), sum.Rows[2].AmountCents)
	assert.Equal(t, "tB2", sum.Rows[2].TxnID)
	assert.Equal(t, 0, sum.ExitCode())
	assert.False(t, sum.DryRun)
	assert.Nil(t, sum.StoppedEarly)

	// YNAB budget: one categories, one accounts (accounts are mapped), one
	// transactions list, then one PUT per charge.
	assert.Len(t, h.srv.Calls(http.MethodGet, categoriesPath), 1)
	assert.Len(t, h.srv.Calls(http.MethodGet, accountsPath), 1)
	require.Equal(t, 1, h.txnListCalls())
	assert.Equal(t, "2026-09-22", h.srv.Calls(http.MethodGet, txnListPath)[0].Query.Get("since_date"),
		"from = now - days - days_before - 1")
	assert.Equal(t, 3, h.srv.WriteCount())

	// The categorizer runs once per order, with only eligible category names.
	require.Len(t, h.cat.calls, 2)
	assert.Equal(t, []string{"Towels", "Apples", "Kibble"}, h.cat.calls[1])
	assert.Equal(t, []string{"Groceries", "Household", "Pets"}, h.cat.allowed[0], "no overrides: YNAB names only")

	// Splits come from the CHARGES' sum, not the order total, filled largest
	// charge first.
	assert.Equal(t, catGroc, sent(t, h.srv.Calls(http.MethodPut, txnPath("tA"))[0])["category_id"])
	assert.Equal(t, [][2]any{{catHome, -63000.0}, {catGroc, -10500.0}},
		subAmounts(t, h.srv.Calls(http.MethodPut, txnPath("tB1"))[0]))
	assert.Equal(t, [][2]any{{catGroc, -21000.0}, {catPets, -10500.0}},
		subAmounts(t, h.srv.Calls(http.MethodPut, txnPath("tB2"))[0]))

	// Pacing: 2s between fetches, none before the first.
	assert.Equal(t, []string{"list", "fetch:oA", "sleep:2s", "fetch:oB"}, h.events)
	assert.Equal(t, []time.Time{now.AddDate(0, 0, -14)}, h.prov.since)
	assert.NotContains(t, h.logs.String(), testToken)
}

func TestRunOrchestrator_pacing_onlyBetweenFetches(t *testing.T) {
	h := newOrchHarness(t)
	for _, id := range []string{"o1", "o2", "o3"} {
		h.prov.add(order.Order{ID: id, DisplayID: id})
	}

	_, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	assert.Equal(t, []string{"list", "fetch:o1", "sleep:2s", "fetch:o2", "sleep:2s", "fetch:o3"}, h.events)
	assert.Equal(t, []time.Duration{PacingDelay, PacingDelay}, h.sleeps)
	assert.Equal(t, 2*time.Second, PacingDelay)
}

func TestRunOrchestrator_rerun_zeroLLMZeroWritesZeroTxnList(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()
	first, err := h.run(t, Options{Days: 14})
	require.NoError(t, err)
	require.Equal(t, []Status{StatusCategorized, StatusSplitInPlace, StatusSplitInPlace}, statuses(first.Rows))
	h.resetCounters()

	second, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusAlreadyProcessed, StatusAlreadyProcessed, StatusAlreadyProcessed}, statuses(second.Rows))
	assert.Equal(t, "tB1", second.Rows[1].TxnID, "the recorded transaction is reported")
	assert.Empty(t, h.cat.calls, "zero categorizer calls")
	assert.Equal(t, 0, h.srv.WriteCount(), "zero YNAB writes")
	assert.Equal(t, 0, h.txnListCalls(), "zero transactions-list calls")
	assert.Equal(t, 0, second.ExitCode())
	t.Logf("rerun counters: categorizer=%d writes=%d txnList=%d", len(h.cat.calls), h.srv.WriteCount(), h.txnListCalls())
}

func TestRunOrchestrator_maxKeepsNewestAndDaysSetsWindow(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()
	h.prov.add(order.Order{ID: "oC", DisplayID: "C-300"})

	sum, err := h.run(t, Options{Days: 30, Max: 2})

	require.NoError(t, err)
	assert.Equal(t, []string{"oA", "oB"}, h.prov.fetched, "the first (newest) two refs only")
	assert.Equal(t, []time.Time{now.AddDate(0, 0, -30)}, h.prov.since)
	assert.Len(t, sum.Rows, 3)
}

func TestRunOrchestrator_dryRun_zeroWritesOfAnyKind(t *testing.T) {
	h := newOrchHarness(t)
	h.happy()

	sum, err := h.run(t, Options{Days: 14, DryRun: true})

	require.NoError(t, err)
	assert.True(t, sum.DryRun)
	require.Equal(t, []Status{StatusCategorized, StatusSplitInPlace, StatusSplitInPlace}, statuses(sum.Rows))
	assert.Equal(t, 0, h.srv.WriteCount(), "zero YNAB writes")
	for _, key := range []string{"walmart:oA:500:1", "walmart:oB:7350:1", "walmart:oB:3150:1"} {
		_, recorded := h.recorded(t, key)
		assert.False(t, recorded, "no store record for %s", key)
	}
	assert.Len(t, h.cat.calls, 2, "a dry run still categorizes")
	assert.Len(t, sum.Rows[1].Splits, 2, "planned splits are reported")
}

func TestRunOrchestrator_force_passesThroughAndRefusesManualMatch(t *testing.T) {
	h := newOrchHarness(t)
	a, tA := orderA()
	n := order.Order{ID: "oN", DisplayID: "N-1", Items: []order.Item{item("Soap", 999)},
		Charges: []order.Charge{charge("oN", 999, 1, date(10, 5))}}
	h.prov.add(a, n)
	h.withTxns(tA)
	h.srv.On(http.MethodPut, txnPath("tA"), okTxn(tA))
	require.NoError(t, h.store.RecordCharge(context.Background(), storage.ChargeRecord{
		Key: "walmart:oN:999:1", OrderID: "oN", YNABTxnID: "sib-9", Outcome: string(NeedsManualMatch), CreatedAt: now,
	}))
	first, err := h.run(t, Options{Days: 14})
	require.NoError(t, err)
	require.Equal(t, []Status{StatusCategorized, StatusAlreadyProcessed}, statuses(first.Rows))
	require.Len(t, h.cat.calls, 1, "the recorded order is not categorized")
	h.resetCounters()

	sum, err := h.run(t, Options{Days: 14, Force: true})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusCategorized, StatusSkipped}, statuses(sum.Rows))
	assert.Len(t, h.cat.calls, 2, "force reconsiders every charge")
	assert.Contains(t, sum.Rows[1].Note, "sib-9")
	assert.Contains(t, sum.Rows[1].Note, "duplicate sibling")
	assert.Equal(t, 1, h.srv.WriteCount(), "only the forced categorize PUT")
}

func TestRunOrchestrator_chargeDates_fallbackFlowsAndZeroDateSkips(t *testing.T) {
	h := newOrchHarness(t)
	fallback := charge("oF", 1234, 1, date(10, 4))
	fallback.DateSource = order.OrderDateFallback
	zero := charge("oZ", 4321, 1, time.Time{})
	zero.DateSource = order.OrderDateFallback
	h.prov.add(
		order.Order{ID: "oF", DisplayID: "F-1", Date: date(10, 4), Items: []order.Item{item("Milk", 1)},
			Charges: []order.Charge{fallback}},
		order.Order{ID: "oZ", DisplayID: "Z-1", Items: []order.Item{item("Milk", 1)},
			Charges: []order.Charge{zero}},
	)
	tF := wm("tF", acctA, -12340, date(10, 6))
	h.withTxns(tF)
	h.srv.On(http.MethodPut, txnPath("tF"), okTxn(tF))

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusCategorized, StatusSkipped}, statuses(sum.Rows))
	assert.Equal(t, "tF", sum.Rows[0].TxnID)
	assert.Contains(t, sum.Rows[1].Note, "no usable date")
	assert.Equal(t, 1, h.srv.WriteCount())
}

func TestRunOrchestrator_overridesAppliedAfterCategorization(t *testing.T) {
	cases := map[string]struct {
		overrides   map[string]string
		model       string
		wantAllowed []string
		wantCatID   string
		wantName    string
	}{
		"override key offered to the categorizer": {
			overrides: map[string]string{"Pet Supplies": "Pets"}, model: "Pet Supplies",
			wantAllowed: []string{"Groceries", "Household", "Pets", "Pet Supplies"},
			wantCatID:   catPets, wantName: "Pets",
		},
		"key equal to a YNAB name in other case is not duplicated": {
			overrides: map[string]string{"pets": "household"}, model: "Pets",
			wantAllowed: []string{"Groceries", "Household", "Pets"},
			wantCatID:   catHome, wantName: "Household",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newOrchHarness(t)
			h.cfg.YNAB.CategoryOverrides = tc.overrides
			h.cat.byItem["Kibble"] = tc.model
			o := order.Order{ID: "oP", DisplayID: "P-1", Items: []order.Item{item("Kibble", 100)},
				Charges: []order.Charge{charge("oP", 100, 1, date(10, 5))}}
			h.prov.add(o)
			tP := wm("tP", acctA, -1000, date(10, 5))
			h.withTxns(tP)
			h.srv.On(http.MethodPut, txnPath("tP"), okTxn(tP))

			sum, err := h.run(t, Options{Days: 14})

			require.NoError(t, err)
			require.Len(t, h.cat.allowed, 1)
			assert.Equal(t, tc.wantAllowed, h.cat.allowed[0])
			require.Equal(t, []Status{StatusCategorized}, statuses(sum.Rows), "row: %+v", sum.Rows)
			assert.Equal(t, tc.wantCatID, sent(t, h.srv.Calls(http.MethodPut, txnPath("tP"))[0])["category_id"])
			assert.Equal(t, tc.wantName, sum.Rows[0].Splits[0].Category)
		})
	}
}

func TestRunOrchestrator_noAccountsMapped_skipsAccountsCall(t *testing.T) {
	h := newOrchHarness(t)
	h.cfg.YNAB.Accounts = nil
	h.cfg.YNAB.CategoryOverrides = map[string]string{"Pet Supplies": "Household"}

	_, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	assert.Empty(t, h.srv.Calls(http.MethodGet, accountsPath))
	assert.Len(t, h.srv.Requests(), 1, "only the categories call")
}
