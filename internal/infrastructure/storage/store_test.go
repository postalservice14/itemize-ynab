package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func tableNames(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version' ORDER BY name`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		out = append(out, n)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestOpen_freshDatabase_runsEmbeddedMigrations(t *testing.T) {
	s := openMem(t)
	assert.Equal(t, []string{"server_knowledge", "ynab_charges", "ynab_txn_cache", "ynab_txn_cache_meta"}, tableNames(t, s))
}

func TestMigrations_downThenUp_bothDirectionsValid(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	p, err := newProvider(s.db)
	require.NoError(t, err)

	_, err = p.DownTo(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, tableNames(t, s))

	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.Len(t, tableNames(t, s), 4)
	// the schema is usable again after the round trip
	require.NoError(t, s.RecordCharge(ctx, ChargeRecord{Key: "k", OrderID: "o", YNABTxnID: "t", Outcome: "split", CreatedAt: time.Now()}))
}

func TestOpen_fileDatabase_persistsAndUsesWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path)
	require.NoError(t, err)
	var mode string
	require.NoError(t, s.db.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&mode))
	assert.Equal(t, "wal", mode)
	var fk int
	require.NoError(t, s.db.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&fk))
	assert.Equal(t, 1, fk)
	require.NoError(t, s.RecordCharge(ctx, ChargeRecord{Key: "k", OrderID: "o", YNABTxnID: "t", Outcome: "split", CreatedAt: time.Now()}))
	require.NoError(t, s.Close())

	s2, err := Open(ctx, path)
	require.NoError(t, err)
	defer func() { _ = s2.Close() }()
	_, ok, err := s2.GetCharge(ctx, "k")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestOpen_badPath_returnsError(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing", "dir", "x.db"))
	require.Error(t, err)
}

func TestOpen_canceledContext_returnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(ctx, ":memory:")
	require.Error(t, err)
}

func TestWithClock_knowledgeUpdatedAtUsesClock(t *testing.T) {
	ctx := context.Background()
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s, err := Open(ctx, ":memory:", WithClock(func() time.Time { return fixed }))
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	require.NoError(t, s.ReplaceCache(ctx, "p", "2026-01-01", nil, 5))
	var got string
	require.NoError(t, s.db.QueryRowContext(context.Background(), `SELECT updated_at FROM server_knowledge WHERE plan_id = 'p'`).Scan(&got))
	assert.Equal(t, "2026-01-02T03:04:05Z", got)
}

func TestCharges_getMissing_returnsNotFound(t *testing.T) {
	_, ok, err := openMem(t).GetCharge(context.Background(), "nope")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCharges_recordThenGet_roundTrips(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	at := time.Date(2026, 5, 6, 7, 8, 9, 123, time.UTC)
	want := ChargeRecord{Key: "k1", OrderID: "o1", YNABTxnID: "t1", Outcome: "split", CreatedAt: at}
	require.NoError(t, s.RecordCharge(ctx, want))
	got, ok, err := s.GetCharge(ctx, "k1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, want.Key, got.Key)
	assert.Equal(t, want.Outcome, got.Outcome)
	assert.True(t, at.Equal(got.CreatedAt))
}

func TestCharges_recordTwice_upsertsOnKey(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	t1 := time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	require.NoError(t, s.RecordCharge(ctx, ChargeRecord{Key: "k", OrderID: "o", YNABTxnID: "a", Outcome: "failed", CreatedAt: t1}))
	require.NoError(t, s.RecordCharge(ctx, ChargeRecord{Key: "k", OrderID: "o2", YNABTxnID: "b", Outcome: "split", CreatedAt: t2}))
	got, ok, err := s.GetCharge(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "o2", got.OrderID)
	assert.Equal(t, "b", got.YNABTxnID)
	assert.Equal(t, "split", got.Outcome)
	assert.True(t, t2.Equal(got.CreatedAt))
	var n int
	require.NoError(t, s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM ynab_charges`).Scan(&n))
	assert.Equal(t, 1, n)
}

func txn(id, date string) CachedTxn {
	return CachedTxn{ID: id, AccountID: "acct", Date: date, Body: []byte(`{"id":"` + id + `"}`)}
}

func ids(rows []CachedTxn) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func TestCacheState_noCache_returnsNotFound(t *testing.T) {
	_, ok, err := openMem(t).CacheState(context.Background(), "p")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestReplaceCache_storesRowsMetaAndKnowledge(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	require.NoError(t, s.ReplaceCache(ctx, "p", "2026-01-01", []CachedTxn{txn("a", "2026-02-01"), txn("b", "2026-01-15")}, 42))

	st, ok, err := s.CacheState(ctx, "p")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, CacheState{SinceDate: "2026-01-01", Knowledge: 42}, st)

	rows, err := s.ListCached(ctx, "p", "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "a"}, ids(rows))
	assert.JSONEq(t, `{"id":"b"}`, string(rows[0].Body))

	// replacing drops rows that are no longer present and moves the bound
	require.NoError(t, s.ReplaceCache(ctx, "p", "2025-12-01", []CachedTxn{txn("c", "2026-03-01")}, 50))
	rows, err = s.ListCached(ctx, "p", "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"c"}, ids(rows))
	st, _, _ = s.CacheState(ctx, "p")
	assert.Equal(t, CacheState{SinceDate: "2025-12-01", Knowledge: 50}, st)
}

func TestReplaceCache_emptyRows_isValidCache(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	require.NoError(t, s.ReplaceCache(ctx, "p", "2026-01-01", nil, 7))
	st, ok, err := s.CacheState(ctx, "p")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(7), st.Knowledge)
}

func TestApplyDelta_mergesUpsertsAndDeletes(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	require.NoError(t, s.ReplaceCache(ctx, "p", "2026-01-01", []CachedTxn{txn("a", "2026-02-01"), txn("b", "2026-02-02")}, 10))

	changed := txn("a", "2026-02-03")
	changed.Body = []byte(`{"id":"a","v":2}`)
	require.NoError(t, s.ApplyDelta(ctx, "p", []CachedTxn{changed, txn("c", "2026-02-04")}, []string{"b"}, 11))

	rows, err := s.ListCached(ctx, "p", "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "c"}, ids(rows))
	assert.JSONEq(t, `{"id":"a","v":2}`, string(rows[0].Body))
	assert.Equal(t, "2026-02-03", rows[0].Date)
	st, _, _ := s.CacheState(ctx, "p")
	assert.Equal(t, int64(11), st.Knowledge)
	assert.Equal(t, "2026-01-01", st.SinceDate)
}

func TestApplyDelta_failureMidway_leavesRowsAndKnowledgeUnchanged(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	require.NoError(t, s.ReplaceCache(ctx, "p", "2026-01-01", []CachedTxn{txn("a", "2026-02-01"), txn("b", "2026-02-02")}, 10))

	bad := CachedTxn{ID: "z", AccountID: "acct", Date: "2026-02-05", Body: nil} // NULL body violates NOT NULL
	err := s.ApplyDelta(ctx, "p", []CachedTxn{txn("new", "2026-02-04"), bad}, []string{"a"}, 99)
	require.Error(t, err)

	rows, err := s.ListCached(ctx, "p", "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, ids(rows), "no partial delta may be visible")
	st, _, _ := s.CacheState(ctx, "p")
	assert.Equal(t, int64(10), st.Knowledge, "knowledge must not advance without its rows")
}

func TestReplaceCache_failureMidway_keepsPreviousCacheAndKnowledge(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	require.NoError(t, s.ReplaceCache(ctx, "p", "2026-01-01", []CachedTxn{txn("a", "2026-02-01")}, 10))

	bad := CachedTxn{ID: "z", AccountID: "acct", Date: "2026-02-05"}
	require.Error(t, s.ReplaceCache(ctx, "p", "2025-06-01", []CachedTxn{txn("n", "2026-02-04"), bad}, 99))

	rows, err := s.ListCached(ctx, "p", "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, ids(rows))
	st, _, _ := s.CacheState(ctx, "p")
	assert.Equal(t, CacheState{SinceDate: "2026-01-01", Knowledge: 10}, st)
}

func TestApplyDelta_canceledContext_changesNothing(t *testing.T) {
	s := openMem(t)
	require.NoError(t, s.ReplaceCache(context.Background(), "p", "2026-01-01", []CachedTxn{txn("a", "2026-02-01")}, 10))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, s.ApplyDelta(ctx, "p", []CachedTxn{txn("b", "2026-02-02")}, nil, 11))
	rows, err := s.ListCached(context.Background(), "p", "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, ids(rows))
	st, _, _ := s.CacheState(context.Background(), "p")
	assert.Equal(t, int64(10), st.Knowledge)
}

func TestListCached_inclusiveBoundsOrderedByDateThenID(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	require.NoError(t, s.ReplaceCache(ctx, "p", "2026-01-01", []CachedTxn{
		txn("b", "2026-02-02"), txn("a", "2026-02-02"), txn("old", "2026-01-31"), txn("late", "2026-02-10"),
	}, 1))

	got, err := s.ListCached(ctx, "p", "2026-02-01", "2026-02-10")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "late"}, ids(got))

	got, err = s.ListCached(ctx, "p", "2026-02-02", "2026-02-02")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, ids(got))

	got, err = s.ListCached(ctx, "p", "2026-02-02", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "late"}, ids(got))
}

func TestCache_isolatedPerPlan(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	require.NoError(t, s.ReplaceCache(ctx, "p1", "2026-01-01", []CachedTxn{txn("a", "2026-02-01")}, 1))
	require.NoError(t, s.ReplaceCache(ctx, "p2", "2026-03-01", []CachedTxn{txn("a", "2026-04-01")}, 2))
	require.NoError(t, s.ApplyDelta(ctx, "p1", nil, []string{"a"}, 3))

	r1, _ := s.ListCached(ctx, "p1", "", "")
	r2, err := s.ListCached(ctx, "p2", "", "")
	require.NoError(t, err)
	assert.Empty(t, r1)
	assert.Equal(t, []string{"a"}, ids(r2))
	st2, _, _ := s.CacheState(ctx, "p2")
	assert.Equal(t, CacheState{SinceDate: "2026-03-01", Knowledge: 2}, st2)
}

func TestHasCachedRows(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	has, err := s.HasCachedRows(ctx, "p1")
	require.NoError(t, err)
	assert.False(t, has, "no cache at all")

	require.NoError(t, s.ReplaceCache(ctx, "p1", "2026-01-01", nil, 1))
	has, err = s.HasCachedRows(ctx, "p1")
	require.NoError(t, err)
	assert.False(t, has, "knowledge but no rows")

	require.NoError(t, s.ReplaceCache(ctx, "p2", "2026-01-01", []CachedTxn{txn("a", "2026-02-01")}, 1))
	has, err = s.HasCachedRows(ctx, "p2")
	require.NoError(t, err)
	assert.True(t, has)
	has, err = s.HasCachedRows(ctx, "p1")
	require.NoError(t, err)
	assert.False(t, has, "rows of another plan do not count")

	require.NoError(t, s.ApplyDelta(ctx, "p2", nil, []string{"a"}, 2))
	has, err = s.HasCachedRows(ctx, "p2")
	require.NoError(t, err)
	assert.False(t, has, "every row deleted")
}

func TestClosedStore_methodsReturnErrors(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	require.NoError(t, s.Close())
	_, _, err := s.GetCharge(ctx, "k")
	require.Error(t, err)
	require.Error(t, s.RecordCharge(ctx, ChargeRecord{Key: "k"}))
	_, _, err = s.CacheState(ctx, "p")
	require.Error(t, err)
	_, err = s.ListCached(ctx, "p", "", "")
	require.Error(t, err)
	_, err = s.HasCachedRows(ctx, "p")
	require.Error(t, err)
	require.Error(t, s.ReplaceCache(ctx, "p", "x", nil, 1))
	require.Error(t, s.ApplyDelta(ctx, "p", nil, nil, 1))
}
