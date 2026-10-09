package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CachedTxn is one cached YNAB transaction. Body is opaque JSON.
type CachedTxn struct {
	ID        string
	AccountID string
	Date      string
	Body      []byte
}

// CacheState describes the cache of one plan.
type CacheState struct {
	// SinceDate is the lower date bound (YYYY-MM-DD) the cache was fetched with.
	SinceDate string
	// Knowledge is the server_knowledge the cache is current as of.
	Knowledge int64
}

// CacheState returns the cache state of the plan; ok is false when none exists.
func (s *Store) CacheState(ctx context.Context, planID string) (CacheState, bool, error) {
	var st CacheState
	err := s.db.QueryRowContext(ctx, `
SELECT m.since_date, k.value
FROM ynab_txn_cache_meta m JOIN server_knowledge k ON k.plan_id = m.plan_id
WHERE m.plan_id = ?`, planID).Scan(&st.SinceDate, &st.Knowledge)
	if errors.Is(err, sql.ErrNoRows) {
		return CacheState{}, false, nil
	}
	if err != nil {
		return CacheState{}, false, fmt.Errorf("storage: cache state %q: %w", planID, err)
	}
	return st, true, nil
}

// ReplaceCache replaces the plan's whole cache with rows, in one transaction:
// either the rows, bound and knowledge all change or none does.
func (s *Store) ReplaceCache(ctx context.Context, planID, sinceDate string, rows []CachedTxn, knowledge int64) error {
	return s.inTx(ctx, "replace cache "+planID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM ynab_txn_cache WHERE plan_id = ?`, planID); err != nil {
			return err
		}
		for _, r := range rows {
			if err := upsertTxn(ctx, tx, planID, r); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO ynab_txn_cache_meta (plan_id, since_date) VALUES (?, ?)
ON CONFLICT(plan_id) DO UPDATE SET since_date = excluded.since_date`, planID, sinceDate); err != nil {
			return err
		}
		return s.setKnowledge(ctx, tx, planID, knowledge)
	})
}

// ApplyDelta merges a delta into the cache in one transaction: upserts and
// deletes are applied and knowledge advances only if all of it commits.
func (s *Store) ApplyDelta(ctx context.Context, planID string, upserts []CachedTxn, deletedIDs []string, knowledge int64) error {
	return s.inTx(ctx, "apply delta "+planID, func(tx *sql.Tx) error {
		for _, r := range upserts {
			if err := upsertTxn(ctx, tx, planID, r); err != nil {
				return err
			}
		}
		for _, id := range deletedIDs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM ynab_txn_cache WHERE plan_id = ? AND txn_id = ?`, planID, id); err != nil {
				return err
			}
		}
		return s.setKnowledge(ctx, tx, planID, knowledge)
	})
}

func (s *Store) inTx(ctx context.Context, what string, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: %s: %w", what, err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("storage: %s: %w", what, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: %s: commit: %w", what, err)
	}
	return nil
}

func upsertTxn(ctx context.Context, tx *sql.Tx, planID string, r CachedTxn) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO ynab_txn_cache (plan_id, txn_id, account_id, date, body) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(plan_id, txn_id) DO UPDATE SET
    account_id = excluded.account_id, date = excluded.date, body = excluded.body`,
		planID, r.ID, r.AccountID, r.Date, r.Body)
	if err != nil {
		return fmt.Errorf("txn %q: %w", r.ID, err)
	}
	return nil
}

func (s *Store) setKnowledge(ctx context.Context, tx *sql.Tx, planID string, knowledge int64) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO server_knowledge (plan_id, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(plan_id) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		planID, knowledge, formatTime(s.clock()))
	return err
}

// ListCached returns the plan's cached transactions dated within [from, to]
// (inclusive, YYYY-MM-DD), ordered by date then id. An empty bound is open.
func (s *Store) ListCached(ctx context.Context, planID, from, to string) ([]CachedTxn, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT txn_id, account_id, date, body FROM ynab_txn_cache
WHERE plan_id = ? AND (? = '' OR date >= ?) AND (? = '' OR date <= ?)
ORDER BY date, txn_id`, planID, from, from, to, to)
	if err != nil {
		return nil, fmt.Errorf("storage: list cached %q: %w", planID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []CachedTxn
	for rows.Next() {
		var r CachedTxn
		if err := rows.Scan(&r.ID, &r.AccountID, &r.Date, &r.Body); err != nil {
			return nil, fmt.Errorf("storage: list cached %q: %w", planID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list cached %q: %w", planID, err)
	}
	return out, nil
}

// HasCachedRows reports whether the plan has at least one cached transaction,
// without reading any of them.
func (s *Store) HasCachedRows(ctx context.Context, planID string) (bool, error) {
	var has bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM ynab_txn_cache WHERE plan_id = ?)`, planID).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("storage: has cached rows %q: %w", planID, err)
	}
	return has, nil
}
