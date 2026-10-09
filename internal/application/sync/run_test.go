package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

func TestRunOrchestrator_storeReadFails_failedRowNoLLM(t *testing.T) {
	h := newOrchHarness(t)
	a, _ := orderA()
	h.prov.add(a)
	o := h.orchestrator(t)
	o.d.Store = &failingStore{Store: h.store, failGet: true}

	sum, err := o.Run(context.Background(), Options{Days: 14})

	require.NoError(t, err)
	require.Equal(t, []Status{StatusFailed}, statuses(sum.Rows))
	assert.Contains(t, sum.Rows[0].Err, "already processed")
	assert.Empty(t, h.cat.calls)
	assert.Equal(t, 0, h.srv.WriteCount())
}

func TestSleepContext(t *testing.T) {
	require.NoError(t, SleepContext(context.Background(), time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, SleepContext(ctx, time.Hour), context.Canceled)
}

func TestShortErr_truncatesByRunes(t *testing.T) {
	long := strings.Repeat("é", maxErrRunes+5)

	got := shortErr(errors.New(long))

	assert.Equal(t, strings.Repeat("é", maxErrRunes)+"...", got)
	assert.Equal(t, "short", shortErr(errors.New("short")))
}

func TestSkippedRow_appendsLedgerNote(t *testing.T) {
	row := skippedRow("D-1", order.SkippedCharge{AmountCents: 250, Reason: order.NonCardPayment, Note: "store credit"})

	assert.Equal(t, Row{OrderDisplayID: "D-1", AmountCents: 250, Status: StatusSkipped, Note: "non-card payment: store credit"}, row)
}
