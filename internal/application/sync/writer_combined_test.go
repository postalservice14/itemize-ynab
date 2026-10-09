package sync

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

func TestPlanCombined_groupsChargesTheBankPostedTogether(t *testing.T) {
	h := newHarness(t)
	bank := wm("bank", acctA, -97720, date(10, 6))
	single := wm("single", acctA, -5700, date(10, 5))
	jobs := []ChargeJob{
		singleJob("k9021", 9021, date(10, 3), acctA),
		singleJob("k570", 570, date(10, 5), acctA),
		singleJob("k751", 751, date(10, 4), acctA),
	}
	w := h.writer(t, []ynab.Transaction{bank, single})

	got := w.PlanCombined(jobs)

	assert.Equal(t, []CombinedMatch{{TxnID: "bank", Jobs: []int{0, 2}}}, got,
		"the $5.70 charge has its own transaction, so it is left for Process")
	res, err := w.Process(context.Background(), singleJob("other", 9772, date(10, 6), acctA))
	require.NoError(t, err)
	assert.NotEqual(t, "bank", res.TxnID, "a planned transaction is claimed")
}

func TestProcessCombined_splitsTheTransactionAndRecordsEveryCharge(t *testing.T) {
	h := newHarness(t)
	orig := wm("bank", acctA, -50000, date(10, 6))
	job := CombineJobs([]ChargeJob{
		singleJob("k1", 2000, date(10, 3), acctA),
		singleJob("k2", 3000, date(10, 4), acctA),
	}, multiJob("ignored", 5000, date(10, 3), acctA).Splits)
	h.srv.On(http.MethodPut, txnPath("bank"), okTxn(savedAs(orig, "[itemize:k1]", job.Splits)))

	res, err := h.writer(t, []ynab.Transaction{orig}).ProcessCombined(context.Background(), job, "bank")

	require.NoError(t, err)
	assert.Equal(t, SplitInPlace, res.Outcome)
	assert.Equal(t, "bank", res.TxnID)
	body := sent(t, h.req(t, 0))
	assert.Equal(t, "[itemize:k1]", body["memo"])
	assert.Equal(t, []map[string]any{
		{"amount": float64(-30000), "category_id": catGroc, "memo": "Milk, Eggs"},
		{"amount": float64(-20000), "category_id": catHome, "memo": "Towels"},
	}, subs(t, body))
	for _, key := range []string{"k1", "k2"} {
		rec, ok := h.recorded(t, key)
		require.True(t, ok, key)
		assert.Equal(t, string(SplitInPlace), rec.Outcome)
		assert.Equal(t, "bank", rec.YNABTxnID)
		assert.Equal(t, "order-"+key, rec.OrderID)
	}
}

func TestProcessCombined_dryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	orig := wm("bank", acctA, -50000, date(10, 6))
	job := CombineJobs([]ChargeJob{
		singleJob("k1", 2000, date(10, 3), acctA),
		singleJob("k2", 3000, date(10, 4), acctA),
	}, multiJob("ignored", 5000, date(10, 3), acctA).Splits)

	res, err := h.writer(t, []ynab.Transaction{orig}, dryRun()).ProcessCombined(context.Background(), job, "bank")

	require.NoError(t, err)
	assert.Equal(t, SplitInPlace, res.Outcome)
	assert.Zero(t, h.srv.WriteCount())
	_, ok := h.recorded(t, "k1")
	assert.False(t, ok)
}

func TestProcessCombined_unknownTransactionIsAnError(t *testing.T) {
	h := newHarness(t)
	job := CombineJobs([]ChargeJob{singleJob("k1", 2000, date(10, 3), acctA), singleJob("k2", 3000, date(10, 4), acctA)},
		singleJob("ignored", 5000, date(10, 3), acctA).Splits)

	_, err := h.writer(t, nil).ProcessCombined(context.Background(), job, "missing")

	require.Error(t, err)
	assert.Zero(t, h.srv.WriteCount())
}

func TestCombineJobs_sumsTheMembersUnderTheFirstKey(t *testing.T) {
	a := singleJob("k1", 2000, date(10, 3), acctA)
	b := singleJob("k2", 3000, date(10, 4), acctA)
	splits := singleJob("x", 5000, date(10, 3), acctA).Splits

	job := CombineJobs([]ChargeJob{a, b}, splits)

	assert.Equal(t, int64(5000), job.Charge.AmountCents)
	assert.Equal(t, "k1", job.Charge.Key)
	assert.Equal(t, a.Charge.Date, job.Charge.Date)
	assert.Equal(t, acctA, job.AccountID)
	assert.Equal(t, a.OrderDisplayID, job.OrderDisplayID)
	assert.Equal(t, []order.Charge{a.Charge, b.Charge}, job.Members)
	assert.Equal(t, splits, job.Splits)
}

func TestProcess_multiChargeOrder_notStaged(t *testing.T) {
	h := newHarness(t)
	job := singleJob("k1", 1234, date(10, 7), acctA)
	job.OrderCharges = 2

	res, err := h.writer(t, nil).Process(context.Background(), job)

	require.NoError(t, err)
	assert.Equal(t, Skipped, res.Outcome)
	assert.Contains(t, res.Note, "several charges")
	assert.Zero(t, h.srv.WriteCount())
	_, ok := h.recorded(t, "k1")
	assert.False(t, ok, "skips are retried next run")
}
