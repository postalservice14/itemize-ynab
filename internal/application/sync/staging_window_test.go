package sync

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

const outsideLoaded = "charge date window starts before the loaded transaction range; widen -days or re-run"

func loadedFrom(m time.Month, d int) func(*Config) {
	return func(c *Config) { c.LoadedFrom = date(m, d) }
}

func TestProcess_noMatch_windowStartsBeforeLoadedRange_skippedWithoutWrites(t *testing.T) {
	h := newHarness(t)
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "st-1"}))

	// Default window: 2 days before. A charge on 10/6 needs transactions
	// from 10/4, but only 10/5 onwards were loaded.
	res, err := h.writer(t, nil, loadedFrom(10, 5)).Process(context.Background(), singleJob("k1", 1234, date(10, 6), acctA))

	require.NoError(t, err)
	assert.Equal(t, Skipped, res.Outcome)
	assert.Contains(t, res.Note, outsideLoaded)
	assert.Empty(t, h.srv.Requests(), "no YNAB write")
	_, recorded := h.recorded(t, "k1")
	assert.False(t, recorded, "skipped charges retry next run")
}

func TestProcess_noMatch_windowInsideLoadedRange_stillStaged(t *testing.T) {
	for name, opts := range map[string][]func(*Config){
		"window starts on the bound": {loadedFrom(10, 5)},
		"zero bound means none":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "st-1"}))

			res, err := h.writer(t, nil, opts...).Process(context.Background(), singleJob("k1", 1234, date(10, 7), acctA))

			require.NoError(t, err)
			assert.Equal(t, StagedForImport, res.Outcome)
			assert.Equal(t, 1, h.srv.WriteCount())
		})
	}
}

func TestRunOrchestrator_chargeOlderThanLoadedTransactions_skippedNotStaged(t *testing.T) {
	h := newOrchHarness(t)
	h.withTxns() // the bank transaction is dated before the loaded range
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "dup"}))
	// -days 3 from 10/9 noon: since 10/6, transactions from 10/3 (days_before 2, plus 1).
	// The ledger charge is dated 10/4, so its window starts 10/2.
	h.prov.add(order.Order{
		ID: "oOld", DisplayID: "OLD-1", Date: date(10, 6),
		Items:   []order.Item{item("Milk", 500)},
		Charges: []order.Charge{charge("oOld", 500, 1, date(10, 4))},
	})

	sum, err := h.run(t, Options{Days: 3})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusSkipped}, statuses(sum.Rows))
	assert.Contains(t, sum.Rows[0].Note, outsideLoaded)
	assert.Equal(t, "2026-10-03", h.srv.Calls(http.MethodGet, txnListPath)[0].Query.Get("since_date"))
	assert.Equal(t, 0, h.srv.WriteCount())
}
