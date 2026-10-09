package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
	"github.com/postalservice14/itemize-ynab/internal/domain/matcher"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/storage"
)

const (
	planBase   = "/plans/" + plan
	createPath = planBase + "/transactions"
	acctA      = "acct-a"
	acctB      = "acct-b"
	flagColor  = "purple"
	catGroc    = "cat-groc"
	catHome    = "cat-home"
	testToken  = "tok-SECRET-123"
)

var now = time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)

func txnPath(id string) string { return planBase + "/transactions/" + id }

func date(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

// harness wires a real ynab.Client to the fake server and a real in-memory store.
type harness struct {
	srv   *ynabtest.Server
	api   *ynab.Client
	store *storage.Store
	logs  *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	srv := ynabtest.New(t)
	return &harness{
		srv:   srv,
		api:   ynab.NewClient(testToken, plan, ynab.WithBaseURL(srv.URL())),
		store: newStore(t),
		logs:  &bytes.Buffer{},
	}
}

func (h *harness) config(opts ...func(*Config)) Config {
	cfg := Config{
		FlagColor:    flagColor,
		MatchOptions: matcher.DefaultOptions(),
		Now:          func() time.Time { return now },
		Logger:       slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

func (h *harness) writer(t *testing.T, txns []ynab.Transaction, opts ...func(*Config)) *Writer {
	t.Helper()
	return h.writerWith(t, h.store, txns, opts...)
}

func (h *harness) writerWith(t *testing.T, store ChargeStore, txns []ynab.Transaction, opts ...func(*Config)) *Writer {
	t.Helper()
	w, err := NewWriter(h.api, store, txns, h.config(opts...))
	require.NoError(t, err)
	return w
}

func (h *harness) recorded(t *testing.T, key string) (storage.ChargeRecord, bool) {
	t.Helper()
	rec, ok, err := h.store.GetCharge(context.Background(), key)
	require.NoError(t, err)
	return rec, ok
}

func mode(m Mode) func(*Config)   { return func(c *Config) { c.SplitInPlace = m } }
func dryRun() func(*Config)       { return func(c *Config) { c.DryRun = true } }
func force() func(*Config)        { return func(c *Config) { c.Force = true } }
func withMemo(memo string) txnOpt { return func(t *ynab.Transaction) { t.Memo = memo } }

type txnOpt func(*ynab.Transaction)

// wm builds a Walmart card transaction as the bank import would deliver it.
func wm(id, acct string, milli int64, d time.Time, opts ...txnOpt) ynab.Transaction {
	t := ynab.Transaction{
		ID: id, AccountID: acct, Amount: milli, Date: ynab.NewDate(d.Date()),
		PayeeName: "Walmart", ImportPayeeNameOriginal: "WAL-MART #1234", Cleared: "cleared",
	}
	for _, o := range opts {
		o(&t)
	}
	return t
}

func sp(cat string, milli int64, memo string) splitter.Split {
	return splitter.Split{CategoryID: cat, AmountMilli: milli, Memo: memo}
}

func chargeJob(key string, cents int64, d time.Time, acct string, splits ...splitter.Split) ChargeJob {
	return ChargeJob{
		Charge:         order.Charge{Key: key, OrderID: "order-" + key, AmountCents: cents, Date: d},
		OrderDisplayID: "D-" + key,
		AccountID:      acct,
		Splits:         splits,
	}
}

// singleJob is a one-category charge whose split memo is "Milk, Eggs".
func singleJob(key string, cents int64, d time.Time, acct string) ChargeJob {
	return chargeJob(key, cents, d, acct, sp(catGroc, -cents*10, "Milk, Eggs"))
}

// multiJob is a two-category charge split 60/40 (cents must be a multiple of 10).
func multiJob(key string, cents int64, d time.Time, acct string) ChargeJob {
	return chargeJob(key, cents, d, acct,
		sp(catGroc, -cents*6, "Milk, Eggs"),
		sp(catHome, -cents*4, "Towels"))
}

func okTxn(t ynab.Transaction) ynabtest.Response { return ynabtest.OK(ynabtest.TransactionData(t)) }

func badRequest() ynabtest.Response {
	return ynabtest.APIError(http.StatusBadRequest, "400", "bad_request", "subtransactions are not supported")
}

func serverError() ynabtest.Response {
	return ynabtest.APIError(http.StatusInternalServerError, "500", "internal_server_error", "boom")
}

// savedAs returns orig as YNAB would echo it after an update with memo and splits.
func savedAs(orig ynab.Transaction, memo string, splits []splitter.Split) ynab.Transaction {
	out := orig
	out.Memo = memo
	if len(splits) > 0 {
		out.CategoryID = nil
	}
	for i, s := range splits {
		cat := s.CategoryID
		out.SubTransactions = append(out.SubTransactions, ynab.SubTransaction{
			ID: orig.ID + "-sub-" + string(rune('a'+i)), TransactionID: orig.ID,
			Amount: s.AmountMilli, Memo: s.Memo, CategoryID: &cat,
		})
	}
	return out
}

// sent decodes the "transaction" object of a request body.
func sent(t *testing.T, r ynabtest.Request) map[string]any {
	t.Helper()
	var env struct {
		Transaction map[string]any `json:"transaction"`
	}
	require.NoError(t, json.Unmarshal(r.Body, &env), "body: %s", r.Body)
	require.NotNil(t, env.Transaction, "body: %s", r.Body)
	return env.Transaction
}

// subs extracts (category, amount) pairs from a sent transaction.
func subs(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["subtransactions"].([]any)
	require.True(t, ok, "no subtransactions in %v", body)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		require.True(t, ok)
		out = append(out, m)
	}
	return out
}

// runAll processes jobs in order and stops at the first error, as the
// orchestrator does.
func runAll(ctx context.Context, w *Writer, jobs ...ChargeJob) ([]Result, error) {
	out := make([]Result, 0, len(jobs))
	for _, j := range jobs {
		r, err := w.Process(ctx, j)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// failingStore wraps a real store and fails the operations it is told to.
type failingStore struct {
	*storage.Store
	failGet    bool
	failRecord bool
}

func (f *failingStore) GetCharge(ctx context.Context, key string) (storage.ChargeRecord, bool, error) {
	if f.failGet {
		return storage.ChargeRecord{}, false, errors.New("disk on fire")
	}
	return f.Store.GetCharge(ctx, key)
}

func (f *failingStore) RecordCharge(ctx context.Context, rec storage.ChargeRecord) error {
	if f.failRecord {
		return errors.New("disk full")
	}
	return f.Store.RecordCharge(ctx, rec)
}

// req returns the i-th recorded request, failing the test when there is none.
func (h *harness) req(t *testing.T, i int) ynabtest.Request {
	t.Helper()
	reqs := h.srv.Requests()
	require.Greater(t, len(reqs), i, "expected at least %d requests", i+1)
	return reqs[i]
}
