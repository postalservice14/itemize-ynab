package sync

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/storage"
)

const (
	planA    = "0a1b2c3d-0000-4000-8000-00000000000a"
	planB    = "0a1b2c3d-0000-4000-8000-00000000000b"
	lastUsed = "last-used"
)

// switchingPlanAPI stands in for YNAB answering "last-used": the plan behind
// the alias changes between runs. A delta request carrying plan A's knowledge
// gets plan A's change back, as the real server did when the alias switched.
type switchingPlanAPI struct {
	current string
	queries []ynab.TransactionsQuery
}

func (f *switchingPlanAPI) ListTransactions(_ context.Context, q ynab.TransactionsQuery) (ynab.TransactionsResult, error) {
	f.queries = append(f.queries, q)
	if q.LastKnowledge == 900 {
		return ynab.TransactionsResult{Transactions: []ynab.Transaction{tx("planA-txn", 2026, 3, 4)}, ServerKnowledge: 901}, nil
	}
	if f.current == planA {
		return ynab.TransactionsResult{Transactions: []ynab.Transaction{tx("planA-txn", 2026, 3, 2)}, ServerKnowledge: 900}, nil
	}
	return ynab.TransactionsResult{Transactions: []ynab.Transaction{tx("planB-bank", 2026, 3, 3)}, ServerKnowledge: 40}, nil
}

func assertNoCache(t *testing.T, store *storage.Store, planID string) {
	t.Helper()
	_, found, err := store.CacheState(context.Background(), planID)
	require.NoError(t, err)
	assert.False(t, found, "no knowledge may be saved for %q", planID)
	rows, err := store.ListCached(context.Background(), planID, "", "")
	require.NoError(t, err)
	assert.Empty(t, rows, "no transactions may be cached for %q", planID)
}

func TestLoad_lastUsedSwitchesPlan_secondRunSeesTheNewPlanAndNothingIsCached(t *testing.T) {
	store := newStore(t)
	api := &switchingPlanAPI{current: planA}
	src := NewTransactionSource(api, store, quiet)
	_, err := src.Load(context.Background(), lastUsed, day(2026, 3, 1))
	require.NoError(t, err)

	api.current = planB
	got, err := src.Load(context.Background(), lastUsed, day(2026, 3, 1))

	require.NoError(t, err)
	assert.Equal(t, []string{"planB-bank"}, ids(got), "plan B's own transactions, never plan A's")
	require.Len(t, api.queries, 2)
	assert.Zero(t, api.queries[1].LastKnowledge, "no delta for an alias plan ID")
	assertNoCache(t, store, lastUsed)
}

func TestLoad_nonUUIDPlan_oneFullCallIgnoringAnyExistingCache(t *testing.T) {
	for _, planID := range []string{lastUsed, "default", "plan-1", "6f1e0c3a9b2d4c5e8f7a1b2c3d4e5f60", "6f1e0c3a-9b2d-4c5e-8f7a-1b2c3d4e5f6g"} {
		t.Run(planID, func(t *testing.T) {
			store := newStore(t)
			ctx := context.Background()
			require.NoError(t, store.ReplaceCache(ctx, planID, "2026-03-01",
				[]storage.CachedTxn{{ID: "stale", AccountID: "acct", Date: "2026-03-02", Body: []byte(`{"id":"stale"}`)}}, 77))
			api := &fakeAPI{replies: []reply{ok(100,
				tx("old", 2026, 2, 27), tx("a", 2026, 3, 2), deleted("gone", 2026, 3, 2), tx("b", 2026, 3, 1))}}

			got, err := NewTransactionSource(api, store, quiet).Load(ctx, planID, day(2026, 3, 1))

			require.NoError(t, err)
			assert.Equal(t, []string{"a", "b"}, ids(got), "live rows dated on or after from, as YNAB returned them")
			require.Len(t, api.queries, 1, "exactly one YNAB read")
			assert.Zero(t, api.queries[0].LastKnowledge)
			assert.Equal(t, "2026-03-01", api.queries[0].SinceDate.String())
			st, found, err := store.CacheState(ctx, planID)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, int64(77), st.Knowledge, "the cache tables are neither read nor written")
		})
	}
}

func TestLoad_nonUUIDPlan_rateLimitReturnedUntouched(t *testing.T) {
	store := newStore(t)
	limited := &ynab.APIError{Method: "GET", Path: "/transactions", Status: http.StatusTooManyRequests}
	api := &fakeAPI{replies: []reply{fail(limited), ok(1, tx("never", 2026, 3, 2))}}

	got, err := NewTransactionSource(api, store, quiet).Load(context.Background(), lastUsed, day(2026, 3, 1))

	require.ErrorIs(t, err, ynab.ErrRateLimited)
	assert.Same(t, limited, err, "the error is returned as is")
	assert.Nil(t, got)
	assert.Len(t, api.queries, 1, "no retry after a 429")
	assertNoCache(t, store, lastUsed)
}

func TestLoad_uuidPlan_stillUsesTheDeltaCache(t *testing.T) {
	store := newStore(t)
	api := &fakeAPI{replies: []reply{ok(100, tx("a", 2026, 3, 2)), ok(101, tx("b", 2026, 3, 3))}}
	src := NewTransactionSource(api, store, quiet)
	_, err := src.Load(context.Background(), planA, day(2026, 3, 1))
	require.NoError(t, err)

	got, err := src.Load(context.Background(), planA, day(2026, 3, 1))

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, ids(got))
	assert.Equal(t, int64(100), api.queries[1].LastKnowledge)
	st, found, err := store.CacheState(context.Background(), planA)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(101), st.Knowledge)
}

func TestIsPlanUUID(t *testing.T) {
	for in, want := range map[string]bool{
		planA:                                   true,
		"6F1E0C3A-9B2D-4C5E-8F7A-1B2C3D4E5F60":  true,
		lastUsed:                                false,
		"default":                               false,
		"":                                      false,
		"6f1e0c3a-9b2d-4c5e-8f7a-1b2c3d4e5f6":   false,
		"6f1e0c3a-9b2d-4c5e-8f7a-1b2c3d4e5f600": false,
		"6f1e0c3a_9b2d-4c5e-8f7a-1b2c3d4e5f60":  false,
		"{6f1e0c3a-9b2d-4c5e-8f7a-1b2c3d4e5f6}": false,
	} {
		assert.Equal(t, want, isPlanUUID(in), in)
	}
}
