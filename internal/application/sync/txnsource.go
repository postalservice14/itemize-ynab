// Package sync holds the use cases that move data between YNAB, the local
// store and the matcher. This file is the transaction source: one YNAB list
// call per run, served from a local cache that deltas are merged into when the
// plan ID is an explicit UUID.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/storage"
)

// TxnAPI is the slice of the YNAB client the source needs.
type TxnAPI interface {
	ListTransactions(ctx context.Context, q ynab.TransactionsQuery) (ynab.TransactionsResult, error)
}

// TxnCache is the slice of the store the source needs; *storage.Store
// satisfies it.
type TxnCache interface {
	CacheState(ctx context.Context, planID string) (storage.CacheState, bool, error)
	ReplaceCache(ctx context.Context, planID, sinceDate string, rows []storage.CachedTxn, knowledge int64) error
	ApplyDelta(ctx context.Context, planID string, upserts []storage.CachedTxn, deletedIDs []string, knowledge int64) error
	ListCached(ctx context.Context, planID, from, to string) ([]storage.CachedTxn, error)
	HasCachedRows(ctx context.Context, planID string) (bool, error)
}

// TransactionSource loads YNAB transactions with a budget of one list call per
// Load. The saved server_knowledge is only ever used together with the cached
// transactions it describes, so a delta can never produce a partial view.
type TransactionSource struct {
	api   TxnAPI
	cache TxnCache
	log   *slog.Logger
}

// NewTransactionSource builds a TransactionSource.
func NewTransactionSource(api TxnAPI, cache TxnCache, log *slog.Logger) *TransactionSource {
	return &TransactionSource{api: api, cache: cache, log: log}
}

// Load returns every transaction dated on or after from. It makes exactly one
// YNAB call: a delta when a usable cache exists, otherwise a full fetch. If the
// delta fails (other than by rate limit or cancellation) it falls back to one
// full fetch. A rate limit is returned untouched, with the cache unchanged.
//
// A plan ID that is not a UUID ("last-used", "default") is an alias whose plan
// can change between runs, so a cache keyed by it could mix two plans. Such a
// plan bypasses the cache entirely: one full fetch, nothing read from or
// written to the cache or knowledge tables.
func (s *TransactionSource) Load(ctx context.Context, planID string, from time.Time) ([]ynab.Transaction, error) {
	fromDate := dateOf(from)
	if !isPlanUUID(planID) {
		s.log.Debug("plan ID is not a UUID; transaction cache bypassed (one full fetch)", "plan_id", planID)
		return s.fetchLive(ctx, fromDate)
	}
	s.log.Debug("plan ID is a UUID; using the transaction cache", "plan_id", planID)
	state, ok, err := s.cache.CacheState(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("load transactions: %w", err)
	}
	if ok && fromDate.String() >= state.SinceDate {
		usable, err := s.hasCachedRows(ctx, planID)
		if err != nil {
			return nil, err
		}
		if usable {
			return s.loadDelta(ctx, planID, fromDate, state)
		}
		s.log.Warn("transaction cache is empty but knowledge is saved; refetching", "plan_id", planID)
	}
	return s.loadFull(ctx, planID, fromDate)
}

func (s *TransactionSource) hasCachedRows(ctx context.Context, planID string) (bool, error) {
	has, err := s.cache.HasCachedRows(ctx, planID)
	if err != nil {
		return false, fmt.Errorf("load transactions: %w", err)
	}
	return has, nil
}

func (s *TransactionSource) loadDelta(ctx context.Context, planID string, from ynab.Date, state storage.CacheState) ([]ynab.Transaction, error) {
	since, err := parseDate(state.SinceDate)
	if err != nil {
		return nil, fmt.Errorf("load transactions: cached since_date: %w", err)
	}
	res, err := s.api.ListTransactions(ctx, ynab.TransactionsQuery{SinceDate: since, LastKnowledge: state.Knowledge})
	if err != nil {
		if errors.Is(err, ynab.ErrRateLimited) || ctx.Err() != nil {
			return nil, err
		}
		s.log.Warn("delta fetch failed; falling back to a full fetch", "plan_id", planID, "error", err)
		return s.loadFull(ctx, planID, from)
	}
	upserts, deleted, err := splitDelta(res.Transactions)
	if err != nil {
		return nil, err
	}
	if err := s.cache.ApplyDelta(ctx, planID, upserts, deleted, res.ServerKnowledge); err != nil {
		return nil, fmt.Errorf("load transactions: merge delta: %w", err)
	}
	s.log.Debug("delta applied", "plan_id", planID, "upserts", len(upserts), "deletes", len(deleted))
	return s.fromCache(ctx, planID, from)
}

// fetchLive makes one full fetch and returns its live transactions dated on or
// after from, without touching the cache.
func (s *TransactionSource) fetchLive(ctx context.Context, from ynab.Date) ([]ynab.Transaction, error) {
	live, _, err := s.fullFetch(ctx, from)
	if err != nil {
		return nil, err
	}
	out := live[:0]
	for _, t := range live {
		if t.Date.String() >= from.String() {
			out = append(out, t)
		}
	}
	return out, nil
}

// fullFetch lists every transaction since from and drops deleted ones.
func (s *TransactionSource) fullFetch(ctx context.Context, from ynab.Date) ([]ynab.Transaction, int64, error) {
	res, err := s.api.ListTransactions(ctx, ynab.TransactionsQuery{SinceDate: from})
	if err != nil {
		return nil, 0, err
	}
	live := make([]ynab.Transaction, 0, len(res.Transactions))
	for _, t := range res.Transactions {
		if !t.Deleted {
			live = append(live, t)
		}
	}
	return live, res.ServerKnowledge, nil
}

func (s *TransactionSource) loadFull(ctx context.Context, planID string, from ynab.Date) ([]ynab.Transaction, error) {
	live, knowledge, err := s.fullFetch(ctx, from)
	if err != nil {
		return nil, err
	}
	rows, err := toCached(live)
	if err != nil {
		return nil, err
	}
	if err := s.cache.ReplaceCache(ctx, planID, from.String(), rows, knowledge); err != nil {
		// The fetched data is complete, so this run can still use it; the
		// next run will fetch fully again because knowledge did not advance.
		s.log.Warn("could not cache transactions; using fetched data for this run", "plan_id", planID, "error", err)
		return live, nil
	}
	return s.fromCache(ctx, planID, from)
}

func (s *TransactionSource) fromCache(ctx context.Context, planID string, from ynab.Date) ([]ynab.Transaction, error) {
	rows, err := s.cache.ListCached(ctx, planID, from.String(), "")
	if err != nil {
		return nil, fmt.Errorf("load transactions: %w", err)
	}
	out := make([]ynab.Transaction, 0, len(rows))
	for _, r := range rows {
		var t ynab.Transaction
		if err := json.Unmarshal(r.Body, &t); err != nil {
			return nil, fmt.Errorf("load transactions: cached transaction %q is corrupt: %w", r.ID, err)
		}
		out = append(out, t)
	}
	return out, nil
}

func splitDelta(txns []ynab.Transaction) (upserts []storage.CachedTxn, deleted []string, err error) {
	var live []ynab.Transaction
	for _, t := range txns {
		if t.Deleted {
			deleted = append(deleted, t.ID)
			continue
		}
		live = append(live, t)
	}
	upserts, err = toCached(live)
	return upserts, deleted, err
}

func toCached(txns []ynab.Transaction) ([]storage.CachedTxn, error) {
	out := make([]storage.CachedTxn, 0, len(txns))
	for _, t := range txns {
		body, err := json.Marshal(t)
		if err != nil {
			return nil, fmt.Errorf("load transactions: encode transaction %q: %w", t.ID, err)
		}
		out = append(out, storage.CachedTxn{ID: t.ID, AccountID: t.AccountID, Date: t.Date.String(), Body: body})
	}
	return out, nil
}

func dateOf(t time.Time) ynab.Date {
	y, m, d := t.Date()
	return ynab.NewDate(y, m, d)
}

func parseDate(s string) (ynab.Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return ynab.Date{}, err
	}
	return dateOf(t), nil
}

// isPlanUUID reports whether planID is a canonical UUID (8-4-4-4-12 hex
// digits). Anything else, such as "last-used", is an alias.
func isPlanUUID(planID string) bool {
	if len(planID) != 36 {
		return false
	}
	for i, r := range planID {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHexDigit(r) {
				return false
			}
		}
	}
	return true
}

func isHexDigit(r rune) bool {
	return ('0' <= r && r <= '9') || ('a' <= r && r <= 'f') || ('A' <= r && r <= 'F')
}
