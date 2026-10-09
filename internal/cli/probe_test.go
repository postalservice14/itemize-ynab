package cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
)

const (
	txnPath = "/plans/plan-1/transactions/t1"
)

func plainTxn(memo string) map[string]any {
	return map[string]any{
		"id": "t1", "date": "2026-09-30", "amount": -185830, "memo": memo,
		"account_id": "a1", "payee_name": "Walmart", "category_id": "orig-cat",
		"subtransactions": []any{},
	}
}

func splitTxn(memo string) map[string]any {
	t := plainTxn(memo)
	t["category_id"] = nil
	t["subtransactions"] = []map[string]any{
		{"id": "s1", "amount": -92920, "category_id": "c1"},
		{"id": "s2", "amount": -92910, "category_id": "c3"},
	}
	return t
}

func twoCats() ynabtest.Response { return categoriesReply() }

func scriptProbe(srv *ynabtest.Server, gets []ynabtest.Response, puts ...ynabtest.Response) {
	srv.On("GET", "/plans/plan-1/categories", twoCats())
	srv.On("GET", txnPath, gets...)
	if len(puts) > 0 {
		srv.On("PUT", txnPath, puts...)
	}
}

func okTxn(txn map[string]any) ynabtest.Response { return ynabtest.OK(ynabtest.TransactionData(txn)) }

func TestProbe_requiresYesBeforeWriting(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv, []ynabtest.Response{okTxn(plainTxn(""))})

	r := run(t, srv, "ynab", "probe-split", "t1")

	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "-yes")
	assert.Equal(t, 0, srv.WriteCount())
}

func TestProbe_refusesUnsuitableTransactions(t *testing.T) {
	split := splitTxn("")
	transfer := plainTxn("")
	transfer["transfer_account_id"] = "a9"
	deleted := plainTxn("")
	deleted["deleted"] = true
	zero := plainTxn("")
	zero["amount"] = 0
	for name, txn := range map[string]map[string]any{"split": split, "transfer": transfer, "deleted": deleted, "zero": zero} {
		t.Run(name, func(t *testing.T) {
			srv := ynabtest.New(t)
			scriptProbe(srv, []ynabtest.Response{okTxn(txn)})

			r := run(t, srv, "ynab", "probe-split", "-yes", "t1")

			assert.Equal(t, 1, r.code)
			assert.Equal(t, 0, srv.WriteCount())
		})
	}
}

func TestProbe_needsTwoEligibleCategories(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/categories", ynabtest.OK(ynabtest.CategoriesData(1,
		ynabtest.Group{ID: "g", Name: "Needs", Categories: []ynabtest.Cat{{ID: "c1", Name: "Only"}}})))
	srv.On("GET", txnPath, okTxn(plainTxn("")))

	r := run(t, srv, "ynab", "probe-split", "-yes", "t1")

	assert.Equal(t, 1, r.code)
	assert.Equal(t, 0, srv.WriteCount())
}

func TestProbe_works_printsRequestResponseAndSaysItCannotRevert(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv,
		[]ynabtest.Response{
			okTxn(plainTxn("orig memo")),                 // initial fetch
			okTxn(splitTxn("orig memo [itemize:probe]")), // verify
			okTxn(splitTxn("orig memo")),                 // after revert attempt: still a split
		},
		okTxn(splitTxn("orig memo [itemize:probe]")), // the probe PUT
		ynabtestAPIError400(),                        // revert refused
		okTxn(splitTxn("orig memo")),                 // memo-only restore
	)

	r := run(t, srv, "ynab", "probe-split", "t1", "-yes")

	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "split-in-place WORKS")
	assert.Contains(t, r.stdout, "CANNOT be reverted")
	assert.Contains(t, r.stdout, "PUT /plans/plan-1/transactions/t1")

	puts := srv.Calls("PUT", txnPath)
	require.GreaterOrEqual(t, len(puts), 1)
	var body struct {
		Transaction struct {
			CategoryID      *string `json:"category_id"`
			Memo            string  `json:"memo"`
			SubTransactions []struct {
				Amount     int64  `json:"amount"`
				CategoryID string `json:"category_id"`
			} `json:"subtransactions"`
		} `json:"transaction"`
	}
	require.NoError(t, json.Unmarshal(puts[0].Body, &body))
	assert.Nil(t, body.Transaction.CategoryID)
	assert.Equal(t, "orig memo [itemize:probe]", body.Transaction.Memo)
	require.Len(t, body.Transaction.SubTransactions, 2)
	assert.Equal(t, int64(-185830), body.Transaction.SubTransactions[0].Amount+body.Transaction.SubTransactions[1].Amount)
	assert.NotEqual(t, body.Transaction.SubTransactions[0].CategoryID, body.Transaction.SubTransactions[1].CategoryID)
}

func ynabtestAPIError400() ynabtest.Response {
	return ynabtest.APIError(400, "400", "bad_request", "cannot modify subtransactions")
}

func TestProbe_works_revertSucceedsWhenYNABAllowsIt(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv,
		[]ynabtest.Response{
			okTxn(plainTxn("m")), okTxn(splitTxn("m [itemize:probe]")), okTxn(plainTxn("m")),
		},
		okTxn(splitTxn("m [itemize:probe]")), okTxn(plainTxn("m")),
	)

	r := run(t, srv, "ynab", "probe-split", "-yes", "t1")

	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "split-in-place WORKS")
	assert.Contains(t, r.stdout, "reverted")
	assert.NotContains(t, r.stdout, "CANNOT be reverted")
}

func TestProbe_rejected400(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv,
		[]ynabtest.Response{okTxn(plainTxn("m"))},
		ynabtest.APIError(400, "400", "bad_request", "Subtransactions are not supported"),
	)

	r := run(t, srv, "ynab", "probe-split", "-yes", "t1")

	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "split-in-place REJECTED (400)")
	assert.Contains(t, r.stdout, "Subtransactions are not supported")
	assert.Equal(t, 1, srv.WriteCount(), "nothing to revert after a 400")
}

func TestProbe_ignored200ButNoSubtransactions_revertsMemo(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv,
		[]ynabtest.Response{
			okTxn(plainTxn("m")),                 // initial
			okTxn(plainTxn("m [itemize:probe]")), // verify: memo changed, no split
			okTxn(plainTxn("m")),                 // after revert
		},
		okTxn(plainTxn("m [itemize:probe]")), // 200 with no subtransactions
		okTxn(plainTxn("m")),                 // restore memo
	)

	r := run(t, srv, "ynab", "probe-split", "-yes", "t1")

	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "split-in-place IGNORED (200 but no subtransactions saved)")
	assert.Contains(t, r.stdout, "reverted")
	puts := srv.Calls("PUT", txnPath)
	require.Len(t, puts, 2)
	assert.JSONEq(t, `{"transaction":{"memo":"m","category_id":"orig-cat"}}`, string(puts[1].Body))
}

func TestProbe_rateLimitMidProbeExits3WithoutVerdict(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv, []ynabtest.Response{okTxn(plainTxn("m"))}, ynabtest.RateLimited())

	r := run(t, srv, "ynab", "probe-split", "-yes", "t1")

	assert.Equal(t, 3, r.code)
	assert.NotContains(t, r.stdout, "verdict")
}

func TestProbe_missingTxnIDIsUsageError(t *testing.T) {
	r := run(t, nil, "ynab", "probe-split", "-yes")

	assert.Equal(t, 1, r.code)
}

func TestProbe_works_memoRestoreAlsoRefusedStillReportsVerdict(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv,
		[]ynabtest.Response{okTxn(plainTxn("m")), okTxn(splitTxn("m [itemize:probe]")), okTxn(splitTxn("m [itemize:probe]"))},
		okTxn(splitTxn("m [itemize:probe]")), ynabtestAPIError400(), ynabtestAPIError400(),
	)

	r := run(t, srv, "ynab", "probe-split", "-yes", "t1")

	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "CANNOT be reverted")
	assert.Contains(t, r.stdout, "split-in-place WORKS")
}
