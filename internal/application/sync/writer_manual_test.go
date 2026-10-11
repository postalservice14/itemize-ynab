package sync

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
)

// twoMultiCharges returns two matched multi-category charges (t1, t2). Their
// PUTs are scripted by the caller.
func twoMultiCharges() (txns []ynab.Transaction, jobs []ChargeJob) {
	t1 := wm("t1", acctA, -50000, date(10, 5))
	t2 := wm("t2", acctA, -70000, date(10, 6))
	return []ynab.Transaction{t1, t2}, []ChargeJob{
		multiJob("k1", 5000, date(10, 4), acctA),
		multiJob("k2", 7000, date(10, 6), acctA),
	}
}

func hasSubtransactions(t *testing.T, r ynabtest.Request) bool {
	_, ok := sent(t, r)["subtransactions"]
	return ok
}

func assertNoCreate(t *testing.T, h *harness) {
	t.Helper()
	for _, r := range h.srv.Requests() {
		assert.NotEqual(t, http.MethodPost, r.Method, "the writer never creates a transaction")
	}
}

func TestProcess_split400_flagsOriginalThenAutoSkipsSplitForLaterCharges(t *testing.T) {
	h := newHarness(t)
	txns, jobs := twoMultiCharges()
	h.srv.On(http.MethodPut, txnPath("t1"), badRequest(), okTxn(txns[0]))
	h.srv.On(http.MethodPut, txnPath("t2"), okTxn(txns[1]))
	w := h.writer(t, txns, mode(ModeAuto))

	res, err := runAll(context.Background(), w, jobs...)

	require.NoError(t, err)
	require.Len(t, res, 2)
	assert.Equal(t, NeedsManualMatch, res[0].Outcome)
	assert.Equal(t, "t1", res[0].TxnID)
	assert.Equal(t, manualSplitNote, res[0].Note)
	assert.Equal(t, NeedsManualMatch, res[1].Outcome)
	assert.Equal(t, "t2", res[1].TxnID)

	reqs := h.srv.Requests()
	require.Len(t, reqs, 3, "split PUT and flag PUT on t1, then only a flag PUT on t2")
	assertNoCreate(t, h)
	assert.True(t, hasSubtransactions(t, reqs[0]))
	assert.Equal(t, txnPath("t1"), reqs[1].Path)
	assert.Equal(t, map[string]any{"flag_color": flagColor, "memo": "[itemize:k1]"}, sent(t, reqs[1]), "flag plus marker")
	assert.Equal(t, txnPath("t2"), reqs[2].Path)
	assert.Equal(t, map[string]any{"flag_color": flagColor, "memo": "[itemize:k2]"}, sent(t, reqs[2]))

	for _, k := range []string{"k1", "k2"} {
		rec, ok := h.recorded(t, k)
		require.True(t, ok, k)
		assert.Equal(t, string(NeedsManualMatch), rec.Outcome)
	}
	rec, _ := h.recorded(t, "k1")
	assert.Equal(t, "t1", rec.YNABTxnID, "the record names the flagged bank transaction")
}

func TestProcess_split400_modeAlways_attemptsEveryCharge(t *testing.T) {
	h := newHarness(t)
	txns, jobs := twoMultiCharges()
	h.srv.On(http.MethodPut, txnPath("t1"), badRequest(), okTxn(txns[0]))
	h.srv.On(http.MethodPut, txnPath("t2"), badRequest(), okTxn(txns[1]))

	res, err := runAll(context.Background(), h.writer(t, txns, mode(ModeAlways)), jobs...)

	require.NoError(t, err)
	assert.Equal(t, NeedsManualMatch, res[1].Outcome)
	t2Calls := h.srv.Calls(http.MethodPut, txnPath("t2"))
	require.Len(t, t2Calls, 2)
	assert.True(t, hasSubtransactions(t, t2Calls[0]), "always mode still tries split-in-place")
	assert.Equal(t, 4, h.srv.WriteCount())
	assertNoCreate(t, h)
}

func TestProcess_split200WithoutSubtransactions_restoresMemoThenFlags(t *testing.T) {
	for _, origMemo := range []string{"Costco run", ""} {
		t.Run("memo="+origMemo, func(t *testing.T) {
			h := newHarness(t)
			orig := wm("t1", acctA, -50000, date(10, 5), withMemo(origMemo))
			job := multiJob("k1", 5000, date(10, 4), acctA)
			marked := savedAs(orig, "marked", nil)
			h.srv.On(http.MethodPut, txnPath("t1"), okTxn(marked), okTxn(orig), okTxn(orig))

			res, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)

			require.NoError(t, err)
			assert.Equal(t, NeedsManualMatch, res.Outcome)
			assert.Equal(t, "t1", res.TxnID)
			reqs := h.srv.Requests()
			require.Len(t, reqs, 3, "split PUT, restore PUT, flag PUT")
			assertNoCreate(t, h)
			assert.True(t, hasSubtransactions(t, reqs[0]))
			assert.Equal(t, map[string]any{"memo": origMemo}, sent(t, reqs[1]), "restore carries the original memo")
			assert.Equal(t, map[string]any{"flag_color": flagColor, "memo": memo.AppendMarker(origMemo, "k1")}, sent(t, reqs[2]))
		})
	}
}

func TestProcess_split200Mismatched_restoresMemoReturnsErrorWithoutFlag(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5), withMemo("Costco run"))
	job := multiJob("k1", 5000, date(10, 4), acctA)
	wrong := slices.Clone(job.Splits)
	wrong[0].AmountMilli, wrong[1].AmountMilli = -30010, -19990
	h.srv.On(http.MethodPut, txnPath("t1"),
		okTxn(savedAs(orig, "Costco run [itemize:k1]", wrong)), okTxn(orig))

	_, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "t1")
	assert.Contains(t, err.Error(), "differ")
	reqs := h.srv.Requests()
	require.Len(t, reqs, 2, "split PUT and memo restore only")
	assert.Equal(t, map[string]any{"memo": "Costco run"}, sent(t, reqs[1]))
	assertNoCreate(t, h)
	_, recorded := h.recorded(t, "k1")
	assert.False(t, recorded)
}

func TestProcess_split200Mismatched_restoreFails_errorNamesBothProblems(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5), withMemo("Costco run"))
	job := multiJob("k1", 5000, date(10, 4), acctA)
	wrong := slices.Clone(job.Splits)
	wrong[0].AmountMilli, wrong[1].AmountMilli = -30010, -19990
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(savedAs(orig, "marked", wrong)), serverError())

	_, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "t1")
	assert.Contains(t, err.Error(), "[itemize:k1]")
	assertNoCreate(t, h)
}

func TestProcess_flaggedOriginalCarriesMarker_notTouchedAgainWithoutDatabase(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5), withMemo("Costco run"))
	job := multiJob("k1", 5000, date(10, 4), acctA)
	h.srv.On(http.MethodPut, txnPath("t1"), badRequest(), okTxn(orig))
	_, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)
	require.NoError(t, err)
	flagPut := sent(t, h.req(t, 1))

	// Fresh database (lost), YNAB state as the first run left it.
	flagged := orig
	flagged.FlagColor = flagColor
	flagged.Memo = flagPut["memo"].(string)
	fresh := newHarness(t)

	res, err := fresh.writer(t, []ynab.Transaction{flagged}).Process(context.Background(), job)

	require.NoError(t, err)
	assert.Equal(t, Skipped, res.Outcome)
	assert.Empty(t, fresh.srv.Requests())
}

func TestProcess_flagFails_errorNothingRecorded(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), badRequest(), serverError())

	_, err := h.writer(t, []ynab.Transaction{orig}).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "k1")
	assert.Contains(t, err.Error(), "t1")
	assertNoCreate(t, h)
	_, recorded := h.recorded(t, "k1")
	assert.False(t, recorded, "nothing was flagged, so the charge retries next run")
}

func TestProcess_flagRateLimited_returnsRateLimit(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), badRequest(), ynabtest.RateLimited())

	_, err := h.writer(t, []ynab.Transaction{orig}).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.ErrorIs(t, err, ynab.ErrRateLimited)
	_, recorded := h.recorded(t, "k1")
	assert.False(t, recorded)
}

func TestProcess_flaggedRecordFails_markerStillProtects(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), badRequest(), okTxn(orig))
	store := &failingStore{Store: h.store, failRecord: true}

	res, err := h.writerWith(t, store, []ynab.Transaction{orig}).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.NoError(t, err)
	assert.Equal(t, NeedsManualMatch, res.Outcome)
	assert.Contains(t, res.Note, "not recorded locally")
	assert.Contains(t, h.logs.String(), "memo marker still prevents")
	assert.NotContains(t, h.logs.String(), testToken)
}

func TestProcess_restoreFails_errorNamesTransaction_noFlag(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5), withMemo("Costco run"))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(savedAs(orig, "marked", nil)), serverError())

	_, err := h.writer(t, []ynab.Transaction{orig}).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "t1")
	assert.Contains(t, err.Error(), "[itemize:k1]")
	assertNoCreate(t, h)
	_, recorded := h.recorded(t, "k1")
	assert.False(t, recorded)
}

func TestProcess_splitServerError_returnsErrorWithoutFallback(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), serverError())

	_, err := h.writer(t, []ynab.Transaction{orig}).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.Error(t, err)
	assert.Len(t, h.srv.Requests(), 1)
	_, recorded := h.recorded(t, "k1")
	assert.False(t, recorded)
}

func TestProcess_splitRateLimited_returnsErrorWithoutFallback(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), ynabtest.RateLimited())

	_, err := h.writer(t, []ynab.Transaction{orig}).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.ErrorIs(t, err, ynab.ErrRateLimited)
	assert.Len(t, h.srv.Requests(), 1)
}

func TestProcess_split200Ignored_restoresChangedCategory(t *testing.T) {
	h := newHarness(t)
	cat := "cat-uncategorized"
	orig := wm("t1", acctA, -50000, date(10, 5), func(t *ynab.Transaction) { t.CategoryID = &cat })
	ignored := savedAs(orig, "[itemize:k1]", nil)
	ignored.CategoryID = nil // YNAB applied category_id: null but dropped the split
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(ignored), okTxn(orig), okTxn(orig))

	res, err := h.writer(t, []ynab.Transaction{orig}).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.NoError(t, err)
	assert.Equal(t, NeedsManualMatch, res.Outcome)
	assert.Equal(t, map[string]any{"memo": "", "category_id": cat}, sent(t, h.req(t, 1)))
}

func TestSplitSaved(t *testing.T) {
	job := multiJob("k1", 5000, date(10, 4), acctA)
	orig := wm("t1", acctA, -50000, date(10, 5))
	good := savedAs(orig, "", job.Splits)
	edit := func(f func(*ynab.Transaction)) ynab.Transaction {
		out := good
		out.SubTransactions = append([]ynab.SubTransaction(nil), good.SubTransactions...)
		f(&out)
		return out
	}
	cases := []struct {
		name  string
		saved ynab.Transaction
		want  bool
	}{
		{"exact", good, true},
		{"reordered", edit(func(t *ynab.Transaction) {
			t.SubTransactions[0], t.SubTransactions[1] = t.SubTransactions[1], t.SubTransactions[0]
		}), true},
		{"extra deleted subtransaction ignored", edit(func(t *ynab.Transaction) {
			t.SubTransactions = append(t.SubTransactions, ynab.SubTransaction{ID: "x", Amount: -1, Deleted: true})
		}), true},
		{"no subtransactions", savedAs(orig, "", nil), false},
		{"one missing", edit(func(t *ynab.Transaction) { t.SubTransactions = t.SubTransactions[:1] }), false},
		{"extra live subtransaction", edit(func(t *ynab.Transaction) {
			t.SubTransactions = append(t.SubTransactions, ynab.SubTransaction{ID: "x", Amount: 0})
		}), false},
		{"wrong category", edit(func(t *ynab.Transaction) {
			other := "cat-other"
			t.SubTransactions[0].CategoryID = &other
		}), false},
		{"nil category", edit(func(t *ynab.Transaction) { t.SubTransactions[0].CategoryID = nil }), false},
		{"wrong parent amount", edit(func(t *ynab.Transaction) { t.Amount = -50010 }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, splitSaved(tc.saved, job))
		})
	}
}
