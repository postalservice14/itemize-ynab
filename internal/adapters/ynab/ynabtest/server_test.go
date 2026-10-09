package ynabtest_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
)

func do(t *testing.T, srv *ynabtest.Server, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL()+path, strings.NewReader(body))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

func TestServer_servesSequenceThenRepeatsLast(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/p/accounts", ynabtest.OK(map[string]any{"n": 1}), ynabtest.RateLimited())

	s1, _ := do(t, srv, "GET", "/plans/p/accounts?x=1", "")
	s2, body2 := do(t, srv, "GET", "/plans/p/accounts", "")
	s3, _ := do(t, srv, "GET", "/plans/p/accounts", "")

	assert.Equal(t, []int{200, 429, 429}, []int{s1, s2, s3})
	assert.Contains(t, body2, "too_many_requests")
}

func TestServer_recordsRequestsAndCountsWrites(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("PUT", "/plans/p/transactions/t", ynabtest.OK(ynabtest.TransactionData(map[string]any{"id": "t"})))
	srv.On("POST", "/plans/p/transactions", ynabtest.Response{Status: 201, Body: []byte(`{"data":{}}`)})
	srv.On("GET", "/plans/p/transactions", ynabtest.OK(ynabtest.TransactionsData(5)))

	do(t, srv, "GET", "/plans/p/transactions?since_date=2026-01-01", "")
	do(t, srv, "PUT", "/plans/p/transactions/t", `{"a":1}`)
	do(t, srv, "POST", "/plans/p/transactions", `{"b":2}`)

	reqs := srv.Requests()
	require.Len(t, reqs, 3)
	assert.Equal(t, "2026-01-01", reqs[0].Query.Get("since_date"))
	assert.False(t, reqs[0].IsWrite())
	assert.Equal(t, `{"a":1}`, string(reqs[1].Body))
	assert.Equal(t, 2, srv.WriteCount())
	assert.Len(t, srv.Calls("PUT", "/plans/p/transactions/t"), 1)

	srv.Reset()
	assert.Empty(t, srv.Requests())
	assert.Equal(t, 0, srv.WriteCount())
}

func TestServer_unscriptedRouteIs404AndStillRecorded(t *testing.T) {
	srv := ynabtest.New(t)

	status, body := do(t, srv, "GET", "/plans/p/nothing", "")

	assert.Equal(t, 404, status)
	assert.Contains(t, body, "no route scripted")
	assert.Len(t, srv.Requests(), 1)
}

func TestServer_unencodableBodyIs500(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/x", ynabtest.Response{Status: 200, Body: make(chan int)})

	status, _ := do(t, srv, "GET", "/x", "")

	assert.Equal(t, 500, status)
}

func TestServer_customHeadersAndNilBody(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/x", ynabtest.Response{Status: 204, Header: http.Header{"X-Test": {"1"}}})

	status, body := do(t, srv, "GET", "/x", "")

	assert.Equal(t, 204, status)
	assert.Empty(t, body)
}

func TestCategoriesData_shape(t *testing.T) {
	d := ynabtest.CategoriesData(9, ynabtest.Group{ID: "g", Name: "G", Categories: []ynabtest.Cat{{ID: "c", Name: "C"}}})

	assert.Equal(t, int64(9), d["server_knowledge"])
	assert.Len(t, d["category_groups"], 1)
}
