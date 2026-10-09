package sync

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
)

func TestProcess_noMatch_stagesSingleCategory(t *testing.T) {
	h := newHarness(t)
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "st-1"}))

	res, err := h.writer(t, nil).Process(context.Background(), singleJob("k1", 1234, date(10, 7), acctA))

	require.NoError(t, err)
	assert.Equal(t, StagedForImport, res.Outcome)
	assert.Equal(t, "st-1", res.TxnID)
	require.Equal(t, 1, h.srv.WriteCount())
	assert.Equal(t, map[string]any{
		"account_id":  acctA,
		"date":        "2026-10-07",
		"amount":      float64(-12340),
		"payee_name":  "Walmart",
		"memo":        "Milk, Eggs [itemize:k1]",
		"category_id": catGroc,
		"approved":    false,
	}, sent(t, h.req(t, 0)), "no flag: nothing needs a human")
	rec, ok := h.recorded(t, "k1")
	require.True(t, ok)
	assert.Equal(t, string(StagedForImport), rec.Outcome)
	assert.Equal(t, "st-1", rec.YNABTxnID)
}

func TestProcess_noMatch_stagesSplit(t *testing.T) {
	h := newHarness(t)
	h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "st-1"}))
	other := wm("other", acctB, -50000, date(10, 7)) // same amount, different account

	res, err := h.writer(t, []ynab.Transaction{other}, func(c *Config) { c.Payee = "Walmart Pickup" }).
		Process(context.Background(), multiJob("k1", 5000, date(10, 7), acctA))

	require.NoError(t, err)
	assert.Equal(t, StagedForImport, res.Outcome)
	body := sent(t, h.req(t, 0))
	assert.Equal(t, "[itemize:k1]", body["memo"])
	assert.Equal(t, "Walmart Pickup", body["payee_name"])
	assert.Nil(t, body["category_id"])
	assert.NotContains(t, body, "flag_color")
	assert.Equal(t, false, body["approved"])
	assert.Equal(t, []map[string]any{
		{"amount": float64(-30000), "category_id": catGroc, "memo": "Milk, Eggs"},
		{"amount": float64(-20000), "category_id": catHome, "memo": "Towels"},
	}, subs(t, body))
}

func TestProcess_noMatch_notStaged(t *testing.T) {
	shell := wm("shell", acctA, -12340, date(10, 7))
	shell.PayeeName, shell.ImportPayeeNameOriginal = "Shell", "SHELL OIL 123"
	cases := []struct {
		name string
		job  ChargeJob
		txns []ynab.Transaction
		note string
	}{
		{"no account mapping", singleJob("k1", 1234, date(10, 7), ""), nil, "no account mapping"},
		{"zero date", singleJob("k1", 1234, time.Time{}, acctA), nil, "no usable date"},
		{"too old", singleJob("k1", 1234, date(9, 29), acctA), nil, "too old"},
		{"in the future", singleJob("k1", 1234, date(10, 11), acctA), nil, "future"},
		{"same amount other payee", singleJob("k1", 1234, date(10, 7), acctA), []ynab.Transaction{shell}, "same amount"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)

			res, err := h.writer(t, tc.txns).Process(context.Background(), tc.job)

			require.NoError(t, err)
			assert.Equal(t, Skipped, res.Outcome)
			assert.Contains(t, res.Note, tc.note)
			assert.Empty(t, h.srv.Requests())
			_, recorded := h.recorded(t, "k1")
			assert.False(t, recorded)
		})
	}
}

func TestProcess_noMatch_ageBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		d      time.Time
		staged bool
	}{
		{"nine and a half days old", date(9, 30), true},
		{"tomorrow", date(10, 10), true},
		{"ten and a half days old", date(9, 29), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.srv.On(http.MethodPost, createPath, okTxn(ynab.Transaction{ID: "st-1"}))

			res, err := h.writer(t, nil).Process(context.Background(), singleJob("k1", 1234, tc.d, acctA))

			require.NoError(t, err)
			assert.Equal(t, tc.staged, res.Outcome == StagedForImport, res.Note)
		})
	}
}

func TestProcess_stagingPostFails_nothingRecorded(t *testing.T) {
	h := newHarness(t)
	h.srv.On(http.MethodPost, createPath, serverError())

	_, err := h.writer(t, nil).Process(context.Background(), singleJob("k1", 1234, date(10, 7), acctA))

	require.Error(t, err)
	_, recorded := h.recorded(t, "k1")
	assert.False(t, recorded)
}
