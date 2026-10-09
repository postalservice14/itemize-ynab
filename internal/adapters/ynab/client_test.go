package ynab_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
)

const testToken = "super-secret-token-123"

func newTestClient(t *testing.T, srv *ynabtest.Server) *ynab.Client {
	t.Helper()
	return ynab.NewClient(testToken, "plan-1", ynab.WithBaseURL(srv.URL()))
}

func TestListCategories_requestShapeAndFlattening(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/categories", ynabtest.OK(ynabtest.CategoriesData(42,
		ynabtest.Group{ID: "g1", Name: "Needs", Categories: []ynabtest.Cat{
			{ID: "c1", Name: "Groceries"},
			{ID: "c2", Name: "Old", Hidden: true},
		}},
		ynabtest.Group{ID: "g2", Name: "Retired", Hidden: true, Deleted: true, Categories: []ynabtest.Cat{
			{ID: "c3", Name: "Gone"},
		}},
	)))

	cats, err := newTestClient(t, srv).ListCategories(context.Background())

	require.NoError(t, err)
	require.Len(t, cats, 3)
	assert.Equal(t, ynab.Category{ID: "c1", Name: "Groceries", GroupID: "g1", GroupName: "Needs"}, cats[0])
	assert.True(t, cats[1].Hidden)
	assert.True(t, cats[2].Hidden, "hidden group hides its categories")
	assert.True(t, cats[2].Deleted, "deleted group deletes its categories")

	reqs := srv.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "Bearer "+testToken, reqs[0].Header.Get("Authorization"))
	assert.Equal(t, "/plans/plan-1/categories", reqs[0].Path)
	assert.Equal(t, "application/json", reqs[0].Header.Get("Accept"))
}

func TestListAccounts_decodesFields(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/accounts", ynabtest.OK(map[string]any{
		"accounts": []map[string]any{
			{"id": "a1", "name": "Visa", "type": "creditCard", "closed": false, "deleted": false, "on_budget": true},
			{"id": "a2", "name": "Old", "type": "checking", "closed": true},
		},
		"server_knowledge": 7,
	}))

	accts, err := newTestClient(t, srv).ListAccounts(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []ynab.Account{
		{ID: "a1", Name: "Visa", Type: "creditCard", OnBudget: true},
		{ID: "a2", Name: "Old", Type: "checking", Closed: true},
	}, accts)
}

func TestListTransactions_paramsAndDecoding(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/transactions", ynabtest.OK(ynabtest.TransactionsData(99,
		map[string]any{
			"id": "t1", "date": "2026-09-30", "amount": -185830, "memo": "m", "flag_color": "red",
			"account_id": "a1", "payee_name": "Walmart", "import_payee_name": "WAL-MART #123",
			"import_payee_name_original": "WAL-MART SUPERCENTER", "approved": true,
			"category_id": nil, "transfer_account_id": nil, "deleted": false,
			"subtransactions": []map[string]any{
				{"id": "s1", "amount": -100000, "category_id": "c1", "memo": "x"},
				{"id": "s2", "amount": -85830, "category_id": "c2", "deleted": true},
			},
		},
		map[string]any{"id": "t2", "date": "2026-10-01", "amount": 5000, "transfer_account_id": "a9", "deleted": true},
	)))

	res, err := newTestClient(t, srv).ListTransactions(context.Background(), ynab.TransactionsQuery{
		SinceDate:     ynab.NewDate(2026, time.September, 1),
		LastKnowledge: 55,
	})

	require.NoError(t, err)
	assert.Equal(t, int64(99), res.ServerKnowledge)
	require.Len(t, res.Transactions, 2)
	tx := res.Transactions[0]
	assert.Equal(t, "t1", tx.ID)
	assert.Equal(t, "2026-09-30", tx.Date.String())
	assert.Equal(t, int64(-185830), tx.Amount)
	assert.Equal(t, "red", tx.FlagColor)
	assert.Equal(t, "WAL-MART SUPERCENTER", tx.ImportPayeeNameOriginal)
	assert.Equal(t, "WAL-MART #123", tx.ImportPayeeName)
	assert.True(t, tx.Approved)
	assert.Nil(t, tx.CategoryID)
	require.Len(t, tx.SubTransactions, 2)
	assert.Equal(t, int64(-100000), tx.SubTransactions[0].Amount)
	assert.True(t, tx.IsSplit(), "has live subtransactions")
	assert.True(t, res.Transactions[1].IsTransfer())
	assert.True(t, res.Transactions[1].Deleted)

	q := srv.Requests()[0].Query
	assert.Equal(t, "2026-09-01", q.Get("since_date"))
	assert.Equal(t, "55", q.Get("last_knowledge_of_server"))
}

func TestListTransactions_zeroParamsSendsNoQuery(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/transactions", ynabtest.OK(ynabtest.TransactionsData(1)))

	_, err := newTestClient(t, srv).ListTransactions(context.Background(), ynab.TransactionsQuery{})

	require.NoError(t, err)
	assert.Empty(t, srv.Requests()[0].Query)
}

func TestGetTransaction_decodes(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/transactions/t1", ynabtest.OK(ynabtest.TransactionData(
		map[string]any{"id": "t1", "date": "2026-09-30", "amount": -1000})))

	tx, err := newTestClient(t, srv).GetTransaction(context.Background(), "t1")

	require.NoError(t, err)
	assert.Equal(t, "t1", tx.ID)
}

func TestUpdateTransaction_splitSendsNullCategory(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("PUT", "/plans/plan-1/transactions/t1", ynabtest.OK(ynabtest.TransactionData(
		map[string]any{"id": "t1", "date": "2026-09-30", "amount": -1000})))
	memo := "hello"

	tx, err := newTestClient(t, srv).UpdateTransaction(context.Background(), "t1", ynab.SaveTransaction{
		Memo: &memo,
		SubTransactions: []ynab.SaveSubTransaction{
			{Amount: -600, CategoryID: "c1", Memo: "a"},
			{Amount: -400, CategoryID: "c2"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "t1", tx.ID)
	reqs := srv.Calls("PUT", "/plans/plan-1/transactions/t1")
	require.Len(t, reqs, 1)
	assert.Equal(t, "application/json", reqs[0].Header.Get("Content-Type"))
	assert.JSONEq(t, `{"transaction":{
		"category_id": null,
		"memo": "hello",
		"subtransactions": [
			{"amount": -600, "category_id": "c1", "memo": "a"},
			{"amount": -400, "category_id": "c2"}
		]}}`, string(reqs[0].Body))
}

func TestUpdateTransaction_plainUpdateOmitsCategoryUnlessSet(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("PUT", "/plans/plan-1/transactions/t1", ynabtest.OK(ynabtest.TransactionData(
		map[string]any{"id": "t1", "date": "2026-09-30", "amount": -1000})))
	memo := "only memo"
	cat := "c9"
	c := newTestClient(t, srv)

	_, err := c.UpdateTransaction(context.Background(), "t1", ynab.SaveTransaction{Memo: &memo})
	require.NoError(t, err)
	_, err = c.UpdateTransaction(context.Background(), "t1", ynab.SaveTransaction{Memo: &memo, CategoryID: &cat})
	require.NoError(t, err)

	reqs := srv.Calls("PUT", "/plans/plan-1/transactions/t1")
	require.Len(t, reqs, 2)
	assert.JSONEq(t, `{"transaction":{"memo":"only memo"}}`, string(reqs[0].Body))
	assert.JSONEq(t, `{"transaction":{"memo":"only memo","category_id":"c9"}}`, string(reqs[1].Body))
}

func TestCreateTransaction_postsBodyAndDecodesResponse(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("POST", "/plans/plan-1/transactions", Response201())
	amount := int64(-185830)
	approved := false
	flag := "purple"

	tx, err := newTestClient(t, srv).CreateTransaction(context.Background(), ynab.SaveTransaction{
		AccountID: "a1", Date: ynab.NewDate(2026, time.October, 2), Amount: &amount,
		PayeeName: "Walmart", Memo: ptr("[itemize:k]"), Approved: &approved, FlagColor: &flag,
		Cleared: "uncleared",
		SubTransactions: []ynab.SaveSubTransaction{
			{Amount: -100000, CategoryID: "c1"}, {Amount: -85830, CategoryID: "c2"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "new-1", tx.ID)
	reqs := srv.Calls("POST", "/plans/plan-1/transactions")
	require.Len(t, reqs, 1)
	assert.JSONEq(t, `{"transaction":{
		"account_id":"a1","date":"2026-10-02","amount":-185830,"payee_name":"Walmart",
		"memo":"[itemize:k]","approved":false,"flag_color":"purple","cleared":"uncleared",
		"category_id":null,
		"subtransactions":[{"amount":-100000,"category_id":"c1"},{"amount":-85830,"category_id":"c2"}]}}`,
		string(reqs[0].Body))
}

func Response201() ynabtest.Response {
	return ynabtest.Response{Status: http.StatusCreated, Body: map[string]any{"data": map[string]any{
		"transaction_ids":  []string{"new-1"},
		"transaction":      map[string]any{"id": "new-1", "date": "2026-10-02", "amount": -185830},
		"server_knowledge": 3,
	}}}
}

func ptr[T any](v T) *T { return &v }

func TestRateLimited_429IsSentinelAndNeverRetries(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/categories", ynabtest.RateLimited(), ynabtest.OK(ynabtest.CategoriesData(1)))

	start := time.Now()
	_, err := newTestClient(t, srv).ListCategories(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, ynab.ErrRateLimited)
	var apiErr *ynab.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 429, apiErr.Status)
	assert.Equal(t, "too_many_requests", apiErr.Name)
	assert.Len(t, srv.Requests(), 1, "client must not retry")
	assert.Less(t, time.Since(start), 2*time.Second, "client must not sleep")
}

func TestAPIError_exposesYNABBodyFor4xxAnd5xx(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"bad request", 400, nil},
		{"unauthorized", 401, ynab.ErrUnauthorized},
		{"not found", 404, nil},
		{"server error", 500, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := ynabtest.New(t)
			srv.On("GET", "/plans/plan-1/accounts",
				ynabtest.APIError(tc.status, "id-x", "name_x", "the detail"))

			_, err := newTestClient(t, srv).ListAccounts(context.Background())

			var apiErr *ynab.APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tc.status, apiErr.Status)
			assert.Equal(t, "id-x", apiErr.ID)
			assert.Equal(t, "name_x", apiErr.Name)
			assert.Equal(t, "the detail", apiErr.Detail)
			assert.Contains(t, err.Error(), "the detail")
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			}
			assert.NotErrorIs(t, err, ynab.ErrRateLimited)
		})
	}
}

func TestAPIError_nonJSONBodyStillTyped(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/accounts", ynabtest.Response{Status: 502, Body: "<html>bad gateway</html>"})

	_, err := newTestClient(t, srv).ListAccounts(context.Background())

	var apiErr *ynab.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 502, apiErr.Status)
}

func TestSuccessWithMalformedBody_errors(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/accounts", ynabtest.Response{Status: 200, Body: "not json"})

	_, err := newTestClient(t, srv).ListAccounts(context.Background())

	require.Error(t, err)
	assert.NotErrorIs(t, err, ynab.ErrRateLimited)
}

func TestPlanIDIsPathEscaped(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/last-used/accounts", ynabtest.OK(map[string]any{"accounts": []any{}}))

	_, err := ynab.NewClient(testToken, "last-used", ynab.WithBaseURL(srv.URL())).ListAccounts(context.Background())

	require.NoError(t, err)
}

func TestToken_neverLeaksThroughErrorsOrFormatting(t *testing.T) {
	srv := ynabtest.New(t)
	// A hostile server echoing the Authorization header back in the error detail.
	srv.On("GET", "/plans/plan-1/categories",
		ynabtest.APIError(401, "401", "unauthorized", "bad credentials: Bearer "+testToken))
	c := newTestClient(t, srv)

	_, apiErr := c.ListCategories(context.Background())
	// A transport failure: the server is gone.
	dead := ynab.NewClient(testToken, "plan-1", ynab.WithBaseURL("http://127.0.0.1:1/v1/"+testToken))
	_, netErr := dead.ListCategories(context.Background())
	require.Error(t, apiErr)
	require.Error(t, netErr)

	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("client", "client", c, "err", apiErr)
	logger.Info("client", slog.Any("c2", *c))

	outputs := []string{
		apiErr.Error(), netErr.Error(), buf.String(),
		fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%s", c), //nolint:gocritic // verbs under test
		fmt.Sprintf("%v", *c), fmt.Sprintf("%+v", *c), fmt.Sprintf("%#v", *c),
		fmt.Sprintf("%v", apiErr), fmt.Sprintf("%+v", apiErr), fmt.Sprintf("%#v", apiErr),
		fmt.Sprintf("%v", netErr), fmt.Sprintf("%+v", netErr), fmt.Sprintf("%#v", netErr),
	}
	for i, out := range outputs {
		assert.NotContains(t, out, testToken, "output %d leaked the token", i)
	}
	j, err := json.Marshal(c)
	require.NoError(t, err)
	assert.NotContains(t, string(j), testToken)
}

func TestContextCancellation_returnsContextError(t *testing.T) {
	srv := ynabtest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newTestClient(t, srv).ListCategories(ctx)

	assert.True(t, errors.Is(err, context.Canceled))
	assert.Empty(t, srv.Requests())
}

func TestSaveTransaction_clearCategorySendsNull(t *testing.T) {
	b, err := json.Marshal(ynab.SaveTransaction{ClearCategory: true})

	require.NoError(t, err)
	assert.JSONEq(t, `{"category_id":null}`, string(b))
}
