package sync

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
)

// orderC is a 60/40 order of three charges; the bank posted the first two as
// one $50.00 transaction and the third on its own. Filled largest charge first:
// $30.00 Household, $20.00 = $6.00 Household + $14.00 Groceries, $10.00 Groceries.
func orderC() (order.Order, ynab.Transaction, ynab.Transaction) {
	o := order.Order{
		ID: "oC", DisplayID: "C-300", Date: date(10, 3),
		Items: []order.Item{item("Towels", 6000), item("Apples", 4000)},
		Charges: []order.Charge{
			charge("oC", 2000, 1, date(10, 3)),
			charge("oC", 3000, 1, date(10, 4)),
			charge("oC", 1000, 1, date(10, 4)),
		},
	}
	return o, wm("tC", acctA, -50000, date(10, 6)), wm("tC3", acctA, -10000, date(10, 5))
}

func TestRunOrchestrator_chargesPostedTogether_splitOneTransaction(t *testing.T) {
	h := newOrchHarness(t)
	o, tC, tC3 := orderC()
	h.prov.add(o)
	h.withTxns(tC, tC3)
	h.srv.On(http.MethodPut, txnPath("tC"), okTxn(savedAs(tC, "[itemize:"+o.Charges[0].Key+"]", []splitter.Split{
		sp(catHome, -36000, "Towels"), sp(catGroc, -14000, "Apples"),
	})))
	h.srv.On(http.MethodPut, txnPath("tC3"), okTxn(tC3))

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusSplitInPlace, StatusSplitInPlace, StatusCategorized}, statuses(sum.Rows))
	assert.Equal(t, []int64{2000, 3000, 1000}, []int64{sum.Rows[0].AmountCents, sum.Rows[1].AmountCents, sum.Rows[2].AmountCents},
		"rows stay in charge order")
	assert.Equal(t, []string{"tC", "tC", "tC3"}, []string{sum.Rows[0].TxnID, sum.Rows[1].TxnID, sum.Rows[2].TxnID})
	assert.Equal(t, "paid together with 1 other charge by one $50.00 transaction", sum.Rows[0].Note)
	assert.Equal(t, []SplitView{{Category: "Household", CategoryID: catHome, AmountCents: 3000}},
		sum.Rows[1].Splits, "each row shows its own charge's share")
	assert.Equal(t, [][2]any{{catHome, -36000.0}, {catGroc, -14000.0}},
		subAmounts(t, h.srv.Calls(http.MethodPut, txnPath("tC"))[0]), "the transaction gets its members' shares merged")
	assert.Equal(t, catGroc, sent(t, h.srv.Calls(http.MethodPut, txnPath("tC3"))[0])["category_id"])
	assert.Equal(t, 2, h.srv.WriteCount())
	for i, c := range o.Charges {
		rec, ok := h.recorded(t, c.Key)
		require.True(t, ok, c.Key)
		assert.Equal(t, []string{"tC", "tC", "tC3"}[i], rec.YNABTxnID)
	}

	// A rerun finds every charge recorded and writes nothing.
	h.resetCounters()
	sum, err = h.run(t, Options{Days: 14})
	require.NoError(t, err)
	assert.Equal(t, []Status{StatusAlreadyProcessed, StatusAlreadyProcessed, StatusAlreadyProcessed}, statuses(sum.Rows))
	assert.Zero(t, h.srv.WriteCount())
}

func TestRunOrchestrator_multiChargeOrder_unmatchedChargesWaitForTheBank(t *testing.T) {
	h := newOrchHarness(t)
	o, _, _ := orderC()
	h.prov.add(o)
	h.withTxns()

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	assert.Equal(t, []Status{StatusSkipped, StatusSkipped, StatusSkipped}, statuses(sum.Rows))
	assert.Contains(t, sum.Rows[0].Note, "several charges")
	assert.Zero(t, h.srv.WriteCount(), "nothing is pre-staged")
}

// orderTip is a delivery charged as $50.00 plus a separate $5.00 driver tip.
func orderTip() (order.Order, ynab.Transaction, ynab.Transaction) {
	o := order.Order{
		ID: "oT", DisplayID: "T-400", Date: date(10, 3), TaxCents: 0, TipCents: 500,
		Items: []order.Item{item("Towels", 3000), item("Apples", 2000)},
		Charges: []order.Charge{
			charge("oT", 5000, 1, date(10, 3)),
			charge("oT", 500, 1, date(10, 4)),
		},
	}
	return o, wm("tT", acctA, -50000, date(10, 5)), wm("tTip", acctA, -5000, date(10, 5))
}

func TestRunOrchestrator_tipCategory_tipChargeGoesToIt(t *testing.T) {
	h := newOrchHarness(t)
	h.cfg.YNAB.TipCategory = "pets"
	o, tT, tTip := orderTip()
	h.prov.add(o)
	h.withTxns(tT, tTip)
	h.srv.On(http.MethodPut, txnPath("tT"), okTxn(savedAs(tT, "", []splitter.Split{
		sp(catHome, -30000, "Towels"), sp(catGroc, -20000, "Apples"),
	})))
	h.srv.On(http.MethodPut, txnPath("tTip"), okTxn(tTip))

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	assert.Equal(t, []Status{StatusSplitInPlace, StatusCategorized}, statuses(sum.Rows))
	assert.Equal(t, [][2]any{{catHome, -30000.0}, {catGroc, -20000.0}},
		subAmounts(t, h.srv.Calls(http.MethodPut, txnPath("tT"))[0]), "the tip is not part of the item split")
	body := sent(t, h.srv.Calls(http.MethodPut, txnPath("tTip"))[0])
	assert.Equal(t, catPets, body["category_id"])
	assert.Equal(t, "Driver tip [itemize:"+o.Charges[1].Key+"]", body["memo"])
}

func TestRunOrchestrator_noTipCategory_tipIsSplitByTheItems(t *testing.T) {
	h := newOrchHarness(t)
	o, tT, tTip := orderTip()
	h.prov.add(o)
	h.withTxns(tT, tTip)
	h.srv.On(http.MethodPut, txnPath("tT"), okTxn(savedAs(tT, "", []splitter.Split{
		sp(catHome, -30000, "Towels"), sp(catGroc, -20000, "Apples"),
	})))
	h.srv.On(http.MethodPut, txnPath("tTip"), okTxn(savedAs(tTip, "", []splitter.Split{
		sp(catHome, -3000, "Towels"), sp(catGroc, -2000, "Apples"),
	})))

	sum, err := h.run(t, Options{Days: 14})

	require.NoError(t, err)
	assert.Equal(t, []Status{StatusSplitInPlace, StatusSplitInPlace}, statuses(sum.Rows))
	assert.Equal(t, [][2]any{{catHome, -30000.0}, {catGroc, -20000.0}},
		subAmounts(t, h.srv.Calls(http.MethodPut, txnPath("tT"))[0]))
	assert.Equal(t, [][2]any{{catHome, -3000.0}, {catGroc, -2000.0}},
		subAmounts(t, h.srv.Calls(http.MethodPut, txnPath("tTip"))[0]), "the tip is shared 60/40 like the items")
}
