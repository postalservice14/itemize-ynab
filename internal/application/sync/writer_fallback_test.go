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

// twoMultiCharges scripts two matched multi-category charges (t1, t2) whose
// sibling POSTs answer sib-1 then sib-2. Split PUTs are scripted by the caller.
func twoMultiCharges(h *harness) (txns []ynab.Transaction, jobs []ChargeJob) {
	t1 := wm("t1", acctA, -50000, date(10, 5))
	t2 := wm("t2", acctA, -70000, date(10, 6))
	h.srv.On(http.MethodPost, createPath,
		okTxn(ynab.Transaction{ID: "sib-1"}), okTxn(ynab.Transaction{ID: "sib-2"}))
	return []ynab.Transaction{t1, t2}, []ChargeJob{
		multiJob("k1", 5000, date(10, 4), acctA),
		multiJob("k2", 7000, date(10, 6), acctA),
	}
}

func hasSubtransactions(t *testing.T, r ynabtest.Request) bool {
	_, ok := sent(t, r)["subtransactions"]
	return ok
}

func TestProcess_split400_siblingThenAutoSkipsSplitForLaterCharges(t *testing.T) {
	h := newHarness(t)
	txns, jobs := twoMultiCharges(h)
	h.srv.On(http.MethodPut, txnPath("t1"), badRequest(), okTxn(txns[0]))
	h.srv.On(http.MethodPut, txnPath("t2"), okTxn(txns[1]))
	w := h.writer(t, txns, mode(ModeAuto))

	res, err := runAll(context.Background(), w, jobs...)

	require.NoError(t, err)
	require.Len(t, res, 2)
	assert.Equal(t, NeedsManualMatch, res[0].Outcome)
	assert.Equal(t, "sib-1", res[0].TxnID)
	assert.Equal(t, NeedsManualMatch, res[1].Outcome)
	assert.Equal(t, "sib-2", res[1].TxnID)

	reqs := h.srv.Requests()
	require.Len(t, reqs, 5, "split PUT, POST, flag PUT, then POST, flag PUT")
	assert.Equal(t, []string{"PUT", "POST", "PUT", "POST", "PUT"},
		[]string{reqs[0].Method, reqs[1].Method, reqs[2].Method, reqs[3].Method, reqs[4].Method})
	assert.True(t, hasSubtransactions(t, reqs[0]))

	sibling := sent(t, reqs[1])
	assert.Equal(t, acctA, sibling["account_id"])
	assert.Equal(t, "2026-10-05", sibling["date"], "sibling takes the matched transaction's date")
	assert.Equal(t, float64(-50000), sibling["amount"])
	assert.Equal(t, "Walmart", sibling["payee_name"])
	assert.Equal(t, "[itemize:k1]", sibling["memo"])
	assert.Equal(t, flagColor, sibling["flag_color"])
	assert.Equal(t, false, sibling["approved"])
	assert.Contains(t, sibling, "category_id")
	assert.Nil(t, sibling["category_id"])
	assert.Len(t, subs(t, sibling), 2)

	assert.Equal(t, txnPath("t1"), reqs[2].Path)
	assert.Equal(t, map[string]any{"flag_color": flagColor, "memo": "[itemize:k1]"}, sent(t, reqs[2]), "flag plus marker")
	assert.Equal(t, txnPath("t2"), reqs[4].Path)
	assert.Equal(t, map[string]any{"flag_color": flagColor, "memo": "[itemize:k2]"}, sent(t, reqs[4]))
	assert.Len(t, h.srv.Calls(http.MethodPut, txnPath("t2")), 1, "no split attempt after the switch-off")

	for _, k := range []string{"k1", "k2"} {
		rec, ok := h.recorded(t, k)
		require.True(t, ok, k)
		assert.Equal(t, string(NeedsManualMatch), rec.Outcome)
	}
	rec, _ := h.recorded(t, "k1")
	assert.Equal(t, "sib-1", rec.YNABTxnID)
}

func TestProcess_split400_modeAlways_attemptsEveryCharge(t *testing.T) {
	h := newHarness(t)
	txns, jobs := twoMultiCharges(h)
	h.srv.On(http.MethodPut, txnPath("t1"), badRequest(), okTxn(txns[0]))
	h.srv.On(http.MethodPut, txnPath("t2"), badRequest(), okTxn(txns[1]))

	res, err := runAll(context.Background(), h.writer(t, txns, mode(ModeAlways)), jobs...)

	require.NoError(t, err)
	assert.Equal(t, NeedsManualMatch, res[1].Outcome)
	t2Calls := h.srv.Calls(http.MethodPut, txnPath("t2"))
	require.Len(t, t2Calls, 2)
	assert.True(t, hasSubtransactions(t, t2Calls[0]), "always mode still tries split-in-place")
	assert.Equal(t, 6, h.srv.WriteCount())
}

func TestProcess_modeNever_neverAttemptsSplit(t *testing.T) {
	h := newHarness(t)
	txns, jobs := twoMultiCharges(h)
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(txns[0]))
	h.srv.On(http.MethodPut, txnPath("t2"), okTxn(txns[1]))

	res, err := runAll(context.Background(), h.writer(t, txns, mode(ModeNever)), jobs...)

	require.NoError(t, err)
	assert.Equal(t, NeedsManualMatch, res[0].Outcome)
	assert.Equal(t, 4, h.srv.WriteCount())
	for _, r := range h.srv.Requests() {
		if r.Method == http.MethodPut {
			assert.False(t, hasSubtransactions(t, r))
		}
	}
}

func TestProcess_split200WithoutSubtransactions_restoresMemoThenSibling(t *testing.T) {
	for _, origMemo := range []string{"Costco run", ""} {
		t.Run("memo="+origMemo, func(t *testing.T) {
			h := newHarness(t)
			orig := wm("t1", acctA, -50000, date(10, 5), withMemo(origMemo))
			job := multiJob("k1", 5000, date(10, 4), acctA)
			marked := savedAs(orig, "marked", nil)
			h.srv.On(http.MethodPut, txnPath("t1"), okTxn(marked), okTxn(orig), okTxn(orig))
			h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "sib-1"}))

			res, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)

			require.NoError(t, err)
			assert.Equal(t, NeedsManualMatch, res.Outcome)
			assert.Equal(t, "sib-1", res.TxnID)
			reqs := h.srv.Requests()
			require.Len(t, reqs, 4, "split PUT, restore PUT, sibling POST, flag PUT")
			assert.True(t, hasSubtransactions(t, reqs[0]))
			assert.Equal(t, map[string]any{"memo": origMemo}, sent(t, reqs[1]), "restore carries the original memo")
			assert.Equal(t, http.MethodPost, reqs[2].Method)
			assert.Equal(t, map[string]any{"flag_color": flagColor, "memo": memo.AppendMarker(origMemo, "k1")}, sent(t, reqs[3]))
		})
	}
}

func TestProcess_split200Mismatched_restoresMemoReturnsErrorWithoutSibling(t *testing.T) {
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
	assert.Empty(t, h.srv.Calls(http.MethodPost, createPath), "no sibling next to an unverified split")
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
	assert.Empty(t, h.srv.Calls(http.MethodPost, createPath))
}

func TestProcess_flaggedOriginalCarriesMarker_noSecondSiblingWithoutDatabase(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5), withMemo("Costco run"))
	job := multiJob("k1", 5000, date(10, 4), acctA)
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "sib-1"}))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(orig))
	_, err := h.writer(t, []ynab.Transaction{orig}, mode(ModeNever)).Process(context.Background(), job)
	require.NoError(t, err)
	flagPut := sent(t, h.req(t, 1))
	h.srv.Reset()

	// Fresh database (lost), YNAB state as the first run left it.
	flagged := orig
	flagged.FlagColor = flagColor
	flagged.Memo = flagPut["memo"].(string)
	sibling := wm("sib-1", acctA, -50000, date(10, 5), withMemo("[itemize:k1]"))
	sibling.SubTransactions = []ynab.SubTransaction{{ID: "s1", Amount: -30000}, {ID: "s2", Amount: -20000}}
	fresh := newHarness(t)

	res, err := fresh.writer(t, []ynab.Transaction{flagged, sibling}, mode(ModeNever)).
		Process(context.Background(), job)

	require.NoError(t, err)
	assert.NotEqual(t, NeedsManualMatch, res.Outcome)
	assert.Empty(t, fresh.srv.Calls(http.MethodPost, createPath), "no second sibling")
}

func TestProcess_restoreFails_errorNamesTransaction_noSibling(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5), withMemo("Costco run"))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(savedAs(orig, "marked", nil)), serverError())

	_, err := h.writer(t, []ynab.Transaction{orig}).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "t1")
	assert.Contains(t, err.Error(), "[itemize:k1]")
	assert.Empty(t, h.srv.Calls(http.MethodPost, createPath))
	_, recorded := h.recorded(t, "k1")
	assert.False(t, recorded)
}

func TestProcess_siblingCreatedFlagFails_stillRecorded(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "sib-1"}))
	h.srv.On(http.MethodPut, txnPath("t1"), serverError())

	res, err := h.writer(t, []ynab.Transaction{orig}, mode(ModeNever)).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.NoError(t, err)
	assert.Equal(t, NeedsManualMatch, res.Outcome)
	assert.Equal(t, "sib-1", res.TxnID)
	assert.Contains(t, res.Note, "flag")
	assert.Contains(t, h.logs.String(), "level=WARN")
	rec, ok := h.recorded(t, "k1")
	require.True(t, ok)
	assert.Equal(t, "sib-1", rec.YNABTxnID)
}

func TestProcess_siblingCreatedFlagRateLimited_recordsThenReturnsError(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "sib-1"}))
	h.srv.On(http.MethodPut, txnPath("t1"), ynabtest.RateLimited())

	res, err := h.writer(t, []ynab.Transaction{orig}, mode(ModeNever)).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.ErrorIs(t, err, ynab.ErrRateLimited)
	assert.Equal(t, NeedsManualMatch, res.Outcome)
	rec, ok := h.recorded(t, "k1")
	require.True(t, ok)
	assert.Equal(t, "sib-1", rec.YNABTxnID)
}

func TestProcess_siblingCreatedRecordFails_noteWarnsOfASecondSibling(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "sib-1"}))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(orig))
	store := &failingStore{Store: h.store, failRecord: true}

	res, err := h.writerWith(t, store, []ynab.Transaction{orig}, mode(ModeNever)).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.NoError(t, err)
	assert.Equal(t, NeedsManualMatch, res.Outcome)
	for _, want := range []string{"sibling sib-1", "not recorded locally", "disk full",
		"a rerun will create a second sibling", "delete one of them by hand"} {
		assert.Contains(t, res.Note, want)
	}
	logs := h.logs.String()
	assert.NotContains(t, logs, "memo marker still prevents", "the original carries no marker")
	assert.Contains(t, logs, "a rerun will create a second sibling")
	assert.Contains(t, logs, "sibling_id=sib-1")
	assert.NotContains(t, logs, testToken)
}

func TestProcess_siblingPostFails_nothingRecorded(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	h.srv.On(http.MethodPost, createPath, serverError())

	_, err := h.writer(t, []ynab.Transaction{orig}, mode(ModeNever)).
		Process(context.Background(), multiJob("k1", 5000, date(10, 4), acctA))

	require.Error(t, err)
	assert.Empty(t, h.srv.Calls(http.MethodPut, txnPath("t1")), "original is not flagged without a sibling")
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
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "sib-1"}))

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
