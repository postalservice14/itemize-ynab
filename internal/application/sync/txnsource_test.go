package sync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/storage"
)

// plan is a UUID plan ID: only an explicit plan uses the delta cache.
const plan = "6f1e0c3a-9b2d-4c5e-8f7a-1b2c3d4e5f60"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type reply struct {
	res ynab.TransactionsResult
	err error
}

// fakeAPI replays scripted replies and records every query.
type fakeAPI struct {
	replies []reply
	queries []ynab.TransactionsQuery
}

func (f *fakeAPI) ListTransactions(_ context.Context, q ynab.TransactionsQuery) (ynab.TransactionsResult, error) {
	f.queries = append(f.queries, q)
	if len(f.queries) > len(f.replies) {
		return ynab.TransactionsResult{}, errors.New("unexpected extra YNAB call")
	}
	r := f.replies[len(f.queries)-1]
	return r.res, r.err
}

func ok(knowledge int64, txns ...ynab.Transaction) reply {
	return reply{res: ynab.TransactionsResult{Transactions: txns, ServerKnowledge: knowledge}}
}

func fail(err error) reply { return reply{err: err} }

func tx(id string, y int, m time.Month, d int) ynab.Transaction {
	return ynab.Transaction{ID: id, AccountID: "acct", Date: ynab.NewDate(y, m, d), Amount: -1000, Memo: id}
}

func deleted(id string, y int, m time.Month, d int) ynab.Transaction {
	t := tx(id, y, m, d)
	t.Deleted = true
	return t
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 15, 30, 0, 0, time.UTC) }

func newStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func ids(txns []ynab.Transaction) []string {
	out := make([]string, 0, len(txns))
	for _, t := range txns {
		out = append(out, t.ID)
	}
	return out
}

func state(t *testing.T, s *storage.Store) storage.CacheState {
	t.Helper()
	st, ok, err := s.CacheState(context.Background(), plan)
	require.NoError(t, err)
	require.True(t, ok)
	return st
}

func TestLoad_firstRun_fullFetchWithExplicitSinceDate(t *testing.T) {
	store := newStore(t)
	api := &fakeAPI{replies: []reply{ok(100, tx("a", 2026, 3, 2), tx("b", 2026, 3, 1))}}
	src := NewTransactionSource(api, store, quiet)

	got, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	assert.Equal(t, []string{"b", "a"}, ids(got))
	require.Len(t, api.queries, 1)
	assert.Equal(t, "2026-03-01", api.queries[0].SinceDate.String(), "since_date must be explicit")
	assert.Zero(t, api.queries[0].LastKnowledge)
	assert.Equal(t, storage.CacheState{SinceDate: "2026-03-01", Knowledge: 100}, state(t, store))
}

func TestLoad_secondRun_deltaMergesUpsertsAndDeletes(t *testing.T) {
	store := newStore(t)
	changed := tx("a", 2026, 3, 2)
	changed.Memo = "edited"
	api := &fakeAPI{replies: []reply{
		ok(100, tx("a", 2026, 3, 2), tx("b", 2026, 3, 1), tx("c", 2026, 3, 5)),
		ok(110, changed, tx("d", 2026, 3, 9), deleted("b", 2026, 3, 1)),
	}}
	src := NewTransactionSource(api, store, quiet)
	_, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	got, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	require.Len(t, api.queries, 2, "one call per Load")
	assert.Equal(t, int64(100), api.queries[1].LastKnowledge)
	assert.Equal(t, "2026-03-01", api.queries[1].SinceDate.String())
	assert.Equal(t, []string{"a", "c", "d"}, ids(got))
	assert.Equal(t, "edited", got[0].Memo)
	assert.Equal(t, int64(110), state(t, store).Knowledge)
}

func TestLoad_emptyDelta_returnsCachedRowsWithoutRefetch(t *testing.T) {
	store := newStore(t)
	api := &fakeAPI{replies: []reply{ok(100, tx("a", 2026, 3, 2)), ok(101)}}
	src := NewTransactionSource(api, store, quiet)
	_, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	got, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	assert.Equal(t, []string{"a"}, ids(got))
	assert.Len(t, api.queries, 2)
	assert.Equal(t, int64(101), state(t, store).Knowledge)
}

func TestLoad_laterFrom_stillDeltaAndFiltersByFrom(t *testing.T) {
	store := newStore(t)
	api := &fakeAPI{replies: []reply{ok(100, tx("a", 2026, 3, 2), tx("b", 2026, 3, 10)), ok(101)}}
	src := NewTransactionSource(api, store, quiet)
	_, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	got, err := src.Load(context.Background(), plan, day(2026, 3, 5))
	require.NoError(t, err)

	assert.Equal(t, []string{"b"}, ids(got))
	assert.Equal(t, int64(100), api.queries[1].LastKnowledge)
	assert.Equal(t, "2026-03-01", api.queries[1].SinceDate.String(), "delta keeps the cache's since_date")
}

func TestLoad_fromEarlierThanCacheSince_fullFetch(t *testing.T) {
	store := newStore(t)
	api := &fakeAPI{replies: []reply{
		ok(100, tx("a", 2026, 3, 2)),
		ok(200, tx("old", 2026, 2, 1), tx("a", 2026, 3, 2)),
	}}
	src := NewTransactionSource(api, store, quiet)
	_, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	got, err := src.Load(context.Background(), plan, day(2026, 2, 1))
	require.NoError(t, err)

	require.Len(t, api.queries, 2)
	assert.Zero(t, api.queries[1].LastKnowledge)
	assert.Equal(t, "2026-02-01", api.queries[1].SinceDate.String())
	assert.Equal(t, []string{"old", "a"}, ids(got))
	assert.Equal(t, storage.CacheState{SinceDate: "2026-02-01", Knowledge: 200}, state(t, store))
}

func TestLoad_fullFetchReturningNothing_isValidAndCached(t *testing.T) {
	store := newStore(t)
	api := &fakeAPI{replies: []reply{ok(100)}}
	got, err := NewTransactionSource(api, store, quiet).Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Equal(t, int64(100), state(t, store).Knowledge)
}

func TestLoad_emptyCacheWithKnowledge_fullFetch(t *testing.T) {
	store := newStore(t)
	require.NoError(t, store.ReplaceCache(context.Background(), plan, "2026-03-01", nil, 100))
	api := &fakeAPI{replies: []reply{ok(120, tx("a", 2026, 3, 2))}}

	got, err := NewTransactionSource(api, store, quiet).Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	require.Len(t, api.queries, 1)
	assert.Zero(t, api.queries[0].LastKnowledge, "inconsistent state must not be trusted")
	assert.Equal(t, []string{"a"}, ids(got))
}

func TestLoad_deltaError_fallsBackToOneFullFetch(t *testing.T) {
	store := newStore(t)
	api := &fakeAPI{replies: []reply{
		ok(100, tx("a", 2026, 3, 2)),
		fail(&ynab.APIError{Method: "GET", Path: "/transactions", Status: http.StatusBadRequest}),
		ok(300, tx("z", 2026, 3, 3)),
	}}
	src := NewTransactionSource(api, store, quiet)
	_, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	got, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	require.Len(t, api.queries, 3, "failed delta plus one full fetch")
	assert.Equal(t, int64(100), api.queries[1].LastKnowledge)
	assert.Zero(t, api.queries[2].LastKnowledge)
	assert.Equal(t, "2026-03-01", api.queries[2].SinceDate.String())
	assert.Equal(t, []string{"z"}, ids(got))
	assert.Equal(t, int64(300), state(t, store).Knowledge)
}

func TestLoad_deltaThenFullBothFail_returnsError(t *testing.T) {
	store := newStore(t)
	boom := errors.New("boom")
	api := &fakeAPI{replies: []reply{ok(100, tx("a", 2026, 3, 2)), fail(errors.New("bad knowledge")), fail(boom)}}
	src := NewTransactionSource(api, store, quiet)
	_, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	_, err = src.Load(context.Background(), plan, day(2026, 3, 1))

	require.ErrorIs(t, err, boom)
	assert.Len(t, api.queries, 3)
	assert.Equal(t, int64(100), state(t, store).Knowledge)
}

func TestLoad_rateLimitedOnDelta_returnsImmediatelyAndLeavesStateUntouched(t *testing.T) {
	store := newStore(t)
	limited := &ynab.APIError{Method: "GET", Path: "/transactions", Status: http.StatusTooManyRequests}
	api := &fakeAPI{replies: []reply{ok(100, tx("a", 2026, 3, 2)), fail(limited), ok(999, tx("never", 2026, 3, 3))}}
	src := NewTransactionSource(api, store, quiet)
	_, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	got, err := src.Load(context.Background(), plan, day(2026, 3, 1))

	require.ErrorIs(t, err, ynab.ErrRateLimited)
	assert.Nil(t, got)
	assert.Len(t, api.queries, 2, "no fallback after a 429")
	assert.Equal(t, storage.CacheState{SinceDate: "2026-03-01", Knowledge: 100}, state(t, store))
	rows, err := store.ListCached(context.Background(), plan, "", "")
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestLoad_rateLimitedOnFirstFull_returnsErrorAndCachesNothing(t *testing.T) {
	store := newStore(t)
	limited := &ynab.APIError{Method: "GET", Path: "/transactions", Status: http.StatusTooManyRequests}
	api := &fakeAPI{replies: []reply{fail(limited)}}

	_, err := NewTransactionSource(api, store, quiet).Load(context.Background(), plan, day(2026, 3, 1))

	require.ErrorIs(t, err, ynab.ErrRateLimited)
	_, found, err := store.CacheState(context.Background(), plan)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestLoad_canceledContextOnDelta_noFallback(t *testing.T) {
	store := newStore(t)
	api := &fakeAPI{replies: []reply{ok(100, tx("a", 2026, 3, 2)), fail(context.Canceled), ok(5)}}
	src := NewTransactionSource(api, store, quiet)
	_, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = src.Load(ctx, plan, day(2026, 3, 1))
	require.Error(t, err)
}

// brokenWrites makes cache writes fail while reads still work.
type brokenWrites struct{ *storage.Store }

var errDisk = errors.New("disk full")

func (brokenWrites) ReplaceCache(context.Context, string, string, []storage.CachedTxn, int64) error {
	return errDisk
}

func (brokenWrites) ApplyDelta(context.Context, string, []storage.CachedTxn, []string, int64) error {
	return errDisk
}

func TestLoad_cacheWriteFailsOnFullFetch_returnsFetchedDataAndKeepsKnowledge(t *testing.T) {
	store := newStore(t)
	api := &fakeAPI{replies: []reply{ok(100, tx("a", 2026, 3, 2))}}

	got, err := NewTransactionSource(api, brokenWrites{store}, quiet).Load(context.Background(), plan, day(2026, 3, 1))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, ids(got))
	_, found, err := store.CacheState(context.Background(), plan)
	require.NoError(t, err)
	assert.False(t, found, "knowledge must not advance when the cache write failed")
}

func TestLoad_cacheWriteFailsOnDelta_returnsErrorAndKeepsKnowledge(t *testing.T) {
	store := newStore(t)
	good := NewTransactionSource(&fakeAPI{replies: []reply{ok(100, tx("a", 2026, 3, 2))}}, store, quiet)
	_, err := good.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	api := &fakeAPI{replies: []reply{ok(110, tx("b", 2026, 3, 3))}}
	_, err = NewTransactionSource(api, brokenWrites{store}, quiet).Load(context.Background(), plan, day(2026, 3, 1))

	require.ErrorIs(t, err, errDisk)
	assert.Equal(t, int64(100), state(t, store).Knowledge)
}

// listSpy records the ranges ListCached is asked for.
type listSpy struct {
	*storage.Store
	ranges [][2]string
}

func (l *listSpy) ListCached(ctx context.Context, planID, from, to string) ([]storage.CachedTxn, error) {
	l.ranges = append(l.ranges, [2]string{from, to})
	return l.Store.ListCached(ctx, planID, from, to)
}

func TestLoad_delta_emptinessCheckDoesNotLoadEveryCachedBody(t *testing.T) {
	store := newStore(t)
	api := &fakeAPI{replies: []reply{ok(100, tx("a", 2026, 3, 2), tx("b", 2026, 3, 9)), ok(101)}}
	_, err := NewTransactionSource(api, store, quiet).Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)
	spy := &listSpy{Store: store}
	src := NewTransactionSource(api, spy, quiet)

	got, err := src.Load(context.Background(), plan, day(2026, 3, 5))

	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, ids(got))
	assert.Equal(t, [][2]string{{"2026-03-05", ""}}, spy.ranges, "only the rows the run returns are read")
}

func TestLoad_corruptCachedBody_errorNamesTransactionID(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	require.NoError(t, store.ReplaceCache(ctx, plan, "2026-03-01",
		[]storage.CachedTxn{{ID: "bad-id", AccountID: "acct", Date: "2026-03-02", Body: []byte("{not json")}}, 100))
	api := &fakeAPI{replies: []reply{ok(101)}}

	_, err := NewTransactionSource(api, store, quiet).Load(ctx, plan, day(2026, 3, 1))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad-id")
}

func TestLoad_plansHaveSeparateCaches(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	apiA := &fakeAPI{replies: []reply{ok(1, tx("a", 2026, 3, 2))}}
	apiB := &fakeAPI{replies: []reply{ok(2, tx("b", 2026, 3, 2))}}
	_, err := NewTransactionSource(apiA, store, quiet).Load(ctx, planA, day(2026, 3, 1))
	require.NoError(t, err)
	got, err := NewTransactionSource(apiB, store, quiet).Load(ctx, planB, day(2026, 3, 1))
	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, ids(got))
	assert.Zero(t, apiB.queries[0].LastKnowledge)
}

func TestLoad_againstFakeYNABServer_sendsSinceDateThenKnowledge(t *testing.T) {
	srv := ynabtest.New(t)
	body := func(k int, txns ...ynab.Transaction) ynabtest.Response {
		return ynabtest.Response{Status: 200, Body: map[string]any{"data": map[string]any{"transactions": txns, "server_knowledge": k}}}
	}
	srv.On("GET", planBase+"/transactions", body(7, tx("a", 2026, 3, 2)), body(8, deleted("a", 2026, 3, 2)))
	client := ynab.NewClient("tok", plan, ynab.WithBaseURL(srv.URL()))
	src := NewTransactionSource(client, newStore(t), quiet)

	first, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)
	second, err := src.Load(context.Background(), plan, day(2026, 3, 1))
	require.NoError(t, err)

	calls := srv.Calls("GET", planBase+"/transactions")
	require.Len(t, calls, 2)
	assert.Equal(t, "2026-03-01", calls[0].Query.Get("since_date"))
	assert.Empty(t, calls[0].Query.Get("last_knowledge_of_server"))
	assert.Equal(t, "7", calls[1].Query.Get("last_knowledge_of_server"))
	assert.Equal(t, "2026-03-01", calls[1].Query.Get("since_date"))
	assert.Equal(t, []string{"a"}, ids(first))
	assert.Empty(t, second)
}

func TestLoad_againstFakeYNABServer_429IsNotRetried(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/p1/transactions", ynabtest.Response{Status: 429, Body: map[string]any{"error": map[string]any{"id": "429", "name": "too_many_requests", "detail": "slow down"}}})
	client := ynab.NewClient("tok", "p1", ynab.WithBaseURL(srv.URL()))

	_, err := NewTransactionSource(client, newStore(t), quiet).Load(context.Background(), "p1", day(2026, 3, 1))

	require.ErrorIs(t, err, ynab.ErrRateLimited)
	assert.Len(t, srv.Requests(), 1)
}

func TestLoad_closedStore_returnsError(t *testing.T) {
	store := newStore(t)
	require.NoError(t, store.Close())
	_, err := NewTransactionSource(&fakeAPI{}, store, quiet).Load(context.Background(), plan, day(2026, 3, 1))
	require.Error(t, err)
}
