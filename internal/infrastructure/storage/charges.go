package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ChargeRecord is the idempotency record of one processed charge.
type ChargeRecord struct {
	Key       string
	OrderID   string
	YNABTxnID string
	Outcome   string
	CreatedAt time.Time
}

// GetCharge returns the record for key; ok is false when there is none.
func (s *Store) GetCharge(ctx context.Context, key string) (ChargeRecord, bool, error) {
	var (
		rec     ChargeRecord
		created string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT key, order_id, ynab_txn_id, outcome, created_at FROM ynab_charges WHERE key = ?`, key,
	).Scan(&rec.Key, &rec.OrderID, &rec.YNABTxnID, &rec.Outcome, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return ChargeRecord{}, false, nil
	}
	if err != nil {
		return ChargeRecord{}, false, fmt.Errorf("storage: get charge %q: %w", key, err)
	}
	rec.CreatedAt, err = time.Parse(timeLayout, created)
	if err != nil {
		return ChargeRecord{}, false, fmt.Errorf("storage: charge %q created_at %q: %w", key, created, err)
	}
	return rec, true, nil
}

// RecordCharge inserts the record, or overwrites the existing record with the
// same key (so a forced reprocess replaces the earlier outcome).
func (s *Store) RecordCharge(ctx context.Context, rec ChargeRecord) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO ynab_charges (key, order_id, ynab_txn_id, outcome, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
    order_id = excluded.order_id,
    ynab_txn_id = excluded.ynab_txn_id,
    outcome = excluded.outcome,
    created_at = excluded.created_at`,
		rec.Key, rec.OrderID, rec.YNABTxnID, rec.Outcome, formatTime(rec.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: record charge %q: %w", rec.Key, err)
	}
	return nil
}
