package sync

import (
	"context"
	"errors"
	"math"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
)

func TestProcess_singleCategory_preservesExistingMemo(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -12340, date(10, 5), withMemo("Costco run"))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(orig))
	job := singleJob("walmart:o1:1234:1", 1234, date(10, 4), acctA)

	res, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)

	require.NoError(t, err)
	assert.Equal(t, Categorized, res.Outcome)
	assert.Equal(t, "t1", res.TxnID)
	require.Equal(t, 1, h.srv.WriteCount())
	body := sent(t, h.srv.Calls(http.MethodPut, txnPath("t1"))[0])
	assert.Equal(t, map[string]any{
		"category_id": catGroc,
		"memo":        "Costco run [itemize:walmart:o1:1234:1]",
	}, body)
	rec, ok := h.recorded(t, job.Charge.Key)
	require.True(t, ok)
	assert.Equal(t, "t1", rec.YNABTxnID)
	assert.Equal(t, string(Categorized), rec.Outcome)
	assert.Equal(t, "order-walmart:o1:1234:1", rec.OrderID)
	assert.True(t, rec.CreatedAt.Equal(now))
}

func TestProcess_singleCategory_emptyMemo_usesItemNames(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -12340, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(orig))

	res, err := h.writer(t, []ynab.Transaction{orig}).
		Process(context.Background(), singleJob("k1", 1234, date(10, 4), acctA))

	require.NoError(t, err)
	assert.Equal(t, Categorized, res.Outcome)
	body := sent(t, h.req(t, 0))
	assert.Equal(t, "Milk, Eggs [itemize:k1]", body["memo"])
}

func TestProcess_multiCategory_splitAccepted(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5), withMemo("Costco run"))
	job := multiJob("k1", 5000, date(10, 4), acctA)
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(savedAs(orig, "Costco run [itemize:k1]", job.Splits)))

	res, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)

	require.NoError(t, err)
	assert.Equal(t, SplitInPlace, res.Outcome)
	assert.Equal(t, "t1", res.TxnID)
	assert.Equal(t, job.Splits, res.Splits)
	require.Equal(t, 1, h.srv.WriteCount())
	body := sent(t, h.req(t, 0))
	assert.Equal(t, "Costco run [itemize:k1]", body["memo"])
	assert.Contains(t, body, "category_id")
	assert.Nil(t, body["category_id"])
	assert.Equal(t, []map[string]any{
		{"amount": float64(-30000), "category_id": catGroc, "memo": "Milk, Eggs"},
		{"amount": float64(-20000), "category_id": catHome, "memo": "Towels"},
	}, subs(t, body))
	rec, ok := h.recorded(t, "k1")
	require.True(t, ok)
	assert.Equal(t, string(SplitInPlace), rec.Outcome)
	assert.Equal(t, "t1", rec.YNABTxnID)
}

func TestProcess_multiCategory_emptyMemo_parentGetsMarkerOnly(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -50000, date(10, 5))
	job := multiJob("k1", 5000, date(10, 4), acctA)
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(savedAs(orig, "[itemize:k1]", job.Splits)))

	res, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)

	require.NoError(t, err)
	assert.Equal(t, SplitInPlace, res.Outcome)
	assert.Equal(t, "[itemize:k1]", sent(t, h.req(t, 0))["memo"])
}

func TestProcess_invalidJob_rejectedBeforeAnyRequest(t *testing.T) {
	base := func() ChargeJob { return multiJob("k1", 5000, date(10, 4), acctA) }
	cases := map[string]func(*ChargeJob){
		"splits do not sum":       func(j *ChargeJob) { j.Splits[1].AmountMilli -= 10 },
		"splits sum short":        func(j *ChargeJob) { j.Splits[0].AmountMilli++ },
		"positive split":          func(j *ChargeJob) { j.Splits[0].AmountMilli, j.Splits[1].AmountMilli = -60000, 10000 },
		"zero split":              func(j *ChargeJob) { j.Splits = append(j.Splits, sp(catHome, 0, "")) },
		"empty category":          func(j *ChargeJob) { j.Splits[1].CategoryID = "" },
		"no splits":               func(j *ChargeJob) { j.Splits = nil },
		"zero charge":             func(j *ChargeJob) { j.Charge.AmountCents = 0 },
		"negative charge":         func(j *ChargeJob) { j.Charge.AmountCents = -5000 },
		"empty key":               func(j *ChargeJob) { j.Charge.Key = "" },
		"charge overflows milli":  func(j *ChargeJob) { j.Charge.AmountCents = 1 << 62 },
		"single split wrong size": func(j *ChargeJob) { j.Splits = []splitter.Split{sp(catGroc, -49990, "x")} },
		"int64 wraparound sums to target": func(j *ChargeJob) {
			j.Splits = []splitter.Split{
				sp(catGroc, -5, "a"), sp(catHome, math.MinInt64, "b"),
				sp(catGroc, -math.MaxInt64, "c"), sp(catHome, -49996, "d"),
			}
		},
		"single split below the charge": func(j *ChargeJob) {
			j.Splits = []splitter.Split{sp(catGroc, math.MinInt64, "a"), sp(catHome, -50000, "b")}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			orig := wm("t1", acctA, -50000, date(10, 5))
			h.srv.On(http.MethodPut, txnPath("t1"), okTxn(orig))
			job := base()
			mutate(&job)

			_, err := h.writer(t, []ynab.Transaction{orig}).Process(context.Background(), job)

			require.ErrorIs(t, err, ErrInvalidJob)
			var invalid *InvalidJobError
			require.ErrorAs(t, err, &invalid)
			assert.Empty(t, h.srv.Requests(), "no request may leave with an invalid split")
			_, recorded := h.recorded(t, job.Charge.Key)
			assert.False(t, recorded)
		})
	}
}

func TestProcess_identicalCharges_claimDifferentTransactions(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(map[bool]string{false: "real", true: "dry run"}[dry], func(t *testing.T) {
			h := newHarness(t)
			t1 := wm("t1", acctA, -12340, date(10, 5))
			t2 := wm("t2", acctA, -12340, date(10, 6))
			h.srv.On(http.MethodPut, txnPath("t1"), okTxn(t1))
			h.srv.On(http.MethodPut, txnPath("t2"), okTxn(t2))
			w := h.writer(t, []ynab.Transaction{t2, t1}, func(c *Config) { c.DryRun = dry })

			res, err := runAll(context.Background(), w,
				singleJob("walmart:o1:1234:1", 1234, date(10, 5), acctA),
				singleJob("walmart:o1:1234:2", 1234, date(10, 5), acctA))

			require.NoError(t, err)
			require.Len(t, res, 2)
			assert.Equal(t, []string{"t1", "t2"}, []string{res[0].TxnID, res[1].TxnID})
			assert.Equal(t, []Outcome{Categorized, Categorized}, []Outcome{res[0].Outcome, res[1].Outcome})
		})
	}
}

func TestProcess_identicalCharges_oneTransaction_secondSkipped(t *testing.T) {
	h := newHarness(t)
	t1 := wm("t1", acctA, -12340, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(t1))
	w := h.writer(t, []ynab.Transaction{t1})

	res, err := runAll(context.Background(), w,
		singleJob("walmart:o1:1234:1", 1234, date(10, 5), acctA),
		singleJob("walmart:o1:1234:2", 1234, date(10, 5), acctA))

	require.NoError(t, err)
	assert.Equal(t, Categorized, res[0].Outcome)
	assert.Equal(t, Skipped, res[1].Outcome)
	assert.Contains(t, res[1].Note, "same amount")
	assert.Equal(t, 1, h.srv.WriteCount())
	_, recorded := h.recorded(t, "walmart:o1:1234:2")
	assert.False(t, recorded)
}

func TestProcess_ambiguousAcrossAccounts_skippedWithCandidates(t *testing.T) {
	h := newHarness(t)
	txns := []ynab.Transaction{
		wm("t1", acctA, -12340, date(10, 5)),
		wm("t2", acctB, -12340, date(10, 5)),
	}

	res, err := h.writer(t, txns).Process(context.Background(), singleJob("k1", 1234, date(10, 5), ""))

	require.NoError(t, err)
	assert.Equal(t, Skipped, res.Outcome)
	assert.Contains(t, res.Note, "ambiguous")
	assert.Contains(t, res.Note, "t1")
	assert.Contains(t, res.Note, "t2")
	assert.Empty(t, h.srv.Requests())
	_, recorded := h.recorded(t, "k1")
	assert.False(t, recorded)
}

func TestProcess_ineligibleTransactions_neverTouched(t *testing.T) {
	h := newHarness(t)
	transfer := "acct-savings"
	split := wm("split", acctA, -12340, date(10, 5))
	split.SubTransactions = []ynab.SubTransaction{{ID: "s1", Amount: -12340}}
	txns := []ynab.Transaction{
		wm("marked", acctA, -12340, date(10, 5), withMemo("x [itemize:walmart:o0:1234:1]")),
		split,
		wm("deleted", acctA, -12340, date(10, 5), func(t *ynab.Transaction) { t.Deleted = true }),
		wm("transfer", acctA, -12340, date(10, 5), func(t *ynab.Transaction) { t.TransferAccountID = &transfer }),
	}

	res, err := h.writer(t, txns).Process(context.Background(), singleJob("k1", 1234, date(10, 5), acctA))

	require.NoError(t, err)
	assert.Equal(t, Skipped, res.Outcome)
	assert.Contains(t, res.Note, "same amount")
	assert.Empty(t, h.srv.Requests())
}

func TestProcess_recordFails_chargeStillSucceeds(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -12340, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), okTxn(orig))
	store := &failingStore{Store: h.store, failRecord: true}

	res, err := h.writerWith(t, store, []ynab.Transaction{orig}).
		Process(context.Background(), singleJob("k1", 1234, date(10, 5), acctA))

	require.NoError(t, err)
	assert.Equal(t, Categorized, res.Outcome)
	assert.Contains(t, sent(t, h.req(t, 0))["memo"], "[itemize:k1]")
	assert.Contains(t, h.logs.String(), "level=WARN")
	assert.Contains(t, h.logs.String(), "disk full")
	assert.Contains(t, h.logs.String(), "the memo marker still prevents a duplicate", "the matched transaction carries the marker")
	assert.Contains(t, res.Note, "not recorded locally: disk full")
}

func TestProcess_storeReadFails_noRequest(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -12340, date(10, 5))
	store := &failingStore{Store: h.store, failGet: true}

	_, err := h.writerWith(t, store, []ynab.Transaction{orig}).
		Process(context.Background(), singleJob("k1", 1234, date(10, 5), acctA))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk on fire")
	assert.Empty(t, h.srv.Requests())
}

func TestProcess_categorizeFails_returnsErrorWithoutToken(t *testing.T) {
	h := newHarness(t)
	orig := wm("t1", acctA, -12340, date(10, 5))
	h.srv.On(http.MethodPut, txnPath("t1"), serverError())

	res, err := h.writer(t, []ynab.Transaction{orig}).
		Process(context.Background(), singleJob("k1", 1234, date(10, 5), acctA))

	require.Error(t, err)
	assert.Empty(t, res.Outcome, "no outcome when the write failed")
	var apiErr *ynab.APIError
	assert.True(t, errors.As(err, &apiErr))
	assert.NotContains(t, err.Error(), testToken)
	assert.NotContains(t, h.logs.String(), testToken)
	_, recorded := h.recorded(t, "k1")
	assert.False(t, recorded)
}
