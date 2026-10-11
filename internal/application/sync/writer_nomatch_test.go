package sync

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
)

func TestProcess_noMatch_skippedWithoutWrites(t *testing.T) {
	shell := wm("shell", acctA, -12340, date(10, 7))
	shell.PayeeName, shell.ImportPayeeNameOriginal = "Shell", "SHELL OIL 123"
	cases := []struct {
		name string
		job  ChargeJob
		txns []ynab.Transaction
	}{
		{"single category", singleJob("k1", 1234, date(10, 7), acctA), nil},
		{"several categories", multiJob("k1", 5000, date(10, 7), acctA), nil},
		{"same amount other payee", singleJob("k1", 1234, date(10, 7), acctA), []ynab.Transaction{shell}},
		{"no account mapping", singleJob("k1", 1234, date(10, 7), ""), nil},
		{"old charge", singleJob("k1", 1234, date(8, 1), acctA), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t) // no routes scripted: any request would 404

			res, err := h.writer(t, tc.txns).Process(context.Background(), tc.job)

			require.NoError(t, err)
			assert.Equal(t, Skipped, res.Outcome)
			assert.Equal(t, noMatchNote, res.Note)
			assert.Empty(t, res.TxnID)
			assert.Empty(t, h.srv.Requests(), "a missing bank transaction is never created")
			_, recorded := h.recorded(t, "k1")
			assert.False(t, recorded, "skipped charges retry next run")
		})
	}
}

func TestProcess_zeroDate_skippedWithReason(t *testing.T) {
	h := newHarness(t)

	res, err := h.writer(t, nil).Process(context.Background(), singleJob("k1", 1234, time.Time{}, acctA))

	require.NoError(t, err)
	assert.Equal(t, Skipped, res.Outcome)
	assert.Equal(t, "no usable date for the charge", res.Note)
	assert.Empty(t, h.srv.Requests())
}

func TestProcess_noMatch_dryRun_skipped(t *testing.T) {
	h := newHarness(t)

	res, err := h.writer(t, nil, dryRun()).Process(context.Background(), singleJob("k1", 1234, date(10, 7), acctA))

	require.NoError(t, err)
	assert.Equal(t, Skipped, res.Outcome)
	assert.Empty(t, h.srv.Requests())
}
