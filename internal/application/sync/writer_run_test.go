package sync

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
)

func TestRun_rateLimitMidRun_partialResults(t *testing.T) {
	h := newHarness(t)
	txns := []ynab.Transaction{
		wm("t1", acctA, -10000, date(10, 5)),
		wm("t2", acctA, -20000, date(10, 5)),
		wm("t3", acctA, -30000, date(10, 5)),
	}
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(txns[0]))
	h.srv.On(http.MethodPut, txnPath("t2"), ynabtest.RateLimited())
	h.srv.On(http.MethodPut, txnPath("t3"), okTxn(txns[2]))

	res, err := runAll(context.Background(), h.writer(t, txns),
		singleJob("k1", 1000, date(10, 5), acctA),
		singleJob("k2", 2000, date(10, 5), acctA),
		singleJob("k3", 3000, date(10, 5), acctA))

	require.ErrorIs(t, err, ynab.ErrRateLimited)
	assert.Contains(t, err.Error(), "k2")
	require.Len(t, res, 1)
	assert.Equal(t, Categorized, res[0].Outcome)
	_, ok1 := h.recorded(t, "k1")
	_, ok2 := h.recorded(t, "k2")
	_, ok3 := h.recorded(t, "k3")
	assert.Equal(t, []bool{true, false, false}, []bool{ok1, ok2, ok3})
	assert.Len(t, h.srv.Requests(), 2, "no request after the 429")
	assert.Empty(t, h.srv.Calls(http.MethodPut, txnPath("t3")))
}

// mixedRun is a single-category match, an accepted split and a staged charge.
func mixedRun(h *harness) ([]ynab.Transaction, []ChargeJob) {
	t1 := wm("t1", acctA, -12340, date(10, 5))
	t2 := wm("t2", acctA, -50000, date(10, 5))
	jobs := []ChargeJob{
		singleJob("k1", 1234, date(10, 5), acctA),
		multiJob("k2", 5000, date(10, 5), acctA),
		singleJob("k3", 777, date(10, 7), acctA),
	}
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(t1))
	h.srv.On(http.MethodPut, txnPath("t2"), okTxn(savedAs(t2, "[itemize:k2]", jobs[1].Splits)))
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "st-1"}))
	return []ynab.Transaction{t1, t2}, jobs
}

func outcomes(res []Result) []Outcome {
	out := make([]Outcome, 0, len(res))
	for _, r := range res {
		out = append(out, r.Outcome)
	}
	return out
}

func TestRun_rerunAfterSuccess_zeroWrites(t *testing.T) {
	h := newHarness(t)
	txns, jobs := mixedRun(h)
	first, err := runAll(context.Background(), h.writer(t, txns), jobs...)
	require.NoError(t, err)
	require.Equal(t, []Outcome{Categorized, SplitInPlace, StagedForImport}, outcomes(first))
	h.srv.Reset()

	second, err := runAll(context.Background(), h.writer(t, txns), jobs...)

	require.NoError(t, err)
	assert.Equal(t, []Outcome{AlreadyProcessed, AlreadyProcessed, AlreadyProcessed}, outcomes(second))
	assert.Empty(t, h.srv.Requests(), "a rerun makes no YNAB calls at all")
}

func TestRun_force_reprocessesRecordedCharge_markerStillProtects(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -12340, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(orig))
	job := singleJob("k1", 1234, date(10, 5), acctA)
	_, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)
	require.NoError(t, err)
	h.srv.Reset()

	t.Run("unmarked transaction is written again", func(t *testing.T) {
		res, err := h.writer(t, []ynab.Transaction{orig}, force()).Process(context.Background(), job)
		require.NoError(t, err)
		assert.Equal(t, Categorized, res.Outcome)
		assert.Equal(t, 1, h.srv.WriteCount())
		h.srv.Reset()
	})
	t.Run("marker still protects a written transaction", func(t *testing.T) {
		marked := wm("t1", acctA, -12340, date(10, 5), withMemo("Milk, Eggs [itemize:k1]"))
		res, err := h.writer(t, []ynab.Transaction{marked}, force()).Process(context.Background(), job)
		require.NoError(t, err)
		assert.Equal(t, Skipped, res.Outcome)
		assert.Empty(t, h.srv.Requests())
	})
}

func TestRun_dryRun_zeroWrites_samePlannedOutcomes(t *testing.T) {
	live := newHarness(t)
	txns, jobs := mixedRun(live)
	realRes, err := runAll(context.Background(), live.writer(t, txns), jobs...)
	require.NoError(t, err)

	h := newHarness(t) // no routes scripted: any request would 404
	dry, err := runAll(context.Background(), h.writer(t, txns, dryRun()), jobs...)

	require.NoError(t, err)
	assert.Equal(t, outcomes(realRes), outcomes(dry))
	assert.Empty(t, h.srv.Requests())
	for i, r := range dry {
		assert.True(t, r.DryRun)
		assert.Equal(t, jobs[i].Splits, r.Splits)
		_, recorded := h.recorded(t, jobs[i].Charge.Key)
		assert.False(t, recorded, jobs[i].Charge.Key)
	}
	assert.Equal(t, "t1", dry[0].TxnID)
	assert.Equal(t, "t2", dry[1].TxnID)
	assert.Contains(t, dry[1].Note, "assumes split-in-place")
}

func TestRun_dryRun_siblingPlanned(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))

	res, err := h.writer(t, []ynab.Transaction{orig}, dryRun(), mode(ModeNever)).
		Process(context.Background(), multiJob("k1", 5000, date(10, 5), acctA))

	require.NoError(t, err)
	assert.Equal(t, NeedsManualMatch, res.Outcome)
	assert.True(t, res.DryRun)
	assert.Contains(t, res.Note, "t1")
	assert.Empty(t, h.srv.Requests())
}

func TestRun_dryRun_alreadyProcessedStillReported(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -12340, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(orig))
	job := singleJob("k1", 1234, date(10, 5), acctA)
	_, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)
	require.NoError(t, err)
	h.srv.Reset()

	res, err := h.writer(t, []ynab.Transaction{orig}, dryRun()).Process(context.Background(), job)

	require.NoError(t, err)
	assert.Equal(t, AlreadyProcessed, res.Outcome)
	assert.True(t, res.DryRun)
	assert.Empty(t, h.srv.Requests())
}

func TestRun_force_needsManualMatchRecorded_neverCreatesSecondSibling(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "sib-1"}), okTxn(ynab.Transaction{ID: "sib-2"}))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(orig))
	job := multiJob("k1", 5000, date(10, 4), acctA)
	first, err := h.writer(t, []ynab.Transaction{orig}, mode(ModeNever)).Process(context.Background(), job)
	require.NoError(t, err)
	require.Equal(t, NeedsManualMatch, first.Outcome)
	require.Equal(t, "sib-1", first.TxnID)
	h.srv.Reset()

	// Post-run YNAB state: the original is flagged but unmarked; the sibling is marked.
	flagged := orig
	flagged.FlagColor = flagColor
	sibling := wm("sib-1", acctA, -50000, date(10, 5), withMemo("[itemize:k1]"))
	sibling.SubTransactions = []ynab.SubTransaction{{ID: "s1", Amount: -30000}, {ID: "s2", Amount: -20000}}

	res, err := h.writer(t, []ynab.Transaction{flagged, sibling}, mode(ModeNever), force()).
		Process(context.Background(), job)

	require.NoError(t, err)
	assert.Equal(t, Skipped, res.Outcome)
	assert.Contains(t, res.Note, "sib-1")
	assert.Contains(t, res.Note, "duplicate sibling")
	assert.Empty(t, h.srv.Requests(), "a forced rerun must not create a second sibling")
	rec, ok := h.recorded(t, "k1")
	require.True(t, ok)
	assert.Equal(t, string(NeedsManualMatch), rec.Outcome, "the existing record is kept")
	assert.Equal(t, "sib-1", rec.YNABTxnID)
}

func TestRun_force_storeReadFails_noRequest(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -12340, date(10, 5))
	store := &failingStore{Store: h.store, failGet: true}

	_, err := h.writerWith(t, store, []ynab.Transaction{orig}, force()).
		Process(context.Background(), singleJob("k1", 1234, date(10, 5), acctA))

	require.Error(t, err)
	assert.Empty(t, h.srv.Requests())
}
