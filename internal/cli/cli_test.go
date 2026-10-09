package cli_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
	"github.com/postalservice14/itemize-ynab/internal/cli"
)

const testToken = "cli-secret-token-xyz"

type result struct {
	code   int
	stdout string
	stderr string
}

func run(t *testing.T, srv *ynabtest.Server, args ...string) result {
	t.Helper()
	t.Setenv("YNAB_TOKEN", testToken)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("ynab:\n  token: \"${YNAB_TOKEN}\"\n  plan_id: plan-1\n"), 0o600))
	return runWithConfig(t, srv, cfg, args...)
}

func runWithConfig(t *testing.T, srv *ynabtest.Server, cfg string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	env := cli.Env{Stdout: &out, Stderr: &errOut}
	if srv != nil {
		env.ClientOptions = []ynab.Option{ynab.WithBaseURL(srv.URL())}
	}
	code := cli.Run(context.Background(), append([]string{"-config", cfg}, args...), env)
	r := result{code: code, stdout: out.String(), stderr: errOut.String()}
	assert.NotContains(t, r.stdout, testToken, "token leaked to stdout")
	assert.NotContains(t, r.stderr, testToken, "token leaked to stderr")
	return r
}

func categoriesReply() ynabtest.Response {
	return ynabtest.OK(ynabtest.CategoriesData(1,
		ynabtest.Group{ID: "g0", Name: "Internal Master Category", Categories: []ynabtest.Cat{
			{ID: "i1", Name: "Inflow: Ready to Assign"}}},
		ynabtest.Group{ID: "g1", Name: "Needs", Categories: []ynabtest.Cat{
			{ID: "c1", Name: "Groceries"}, {ID: "c2", Name: "Old Stuff", Hidden: true}, {ID: "c3", Name: "Pets"}}},
		ynabtest.Group{ID: "g2", Name: "Credit Card Payments", Categories: []ynabtest.Cat{
			{ID: "cc", Name: "Visa"}}},
		ynabtest.Group{ID: "g3", Name: "Dead", Deleted: true, Categories: []ynabtest.Cat{
			{ID: "d1", Name: "Gone"}}},
	))
}

func TestCategories_listsAllNonDeletedWithHiddenMarker(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/categories", categoriesReply())

	r := run(t, srv, "ynab", "categories")

	require.Equal(t, 0, r.code, r.stderr)
	for _, want := range []string{"Groceries", "Needs", "c1", "Old Stuff", "hidden", "Inflow: Ready to Assign", "Visa"} {
		assert.Contains(t, r.stdout, want)
	}
	assert.NotContains(t, r.stdout, "Gone")
}

func TestCategories_eligibleFlagFilters(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/categories", categoriesReply())

	r := run(t, srv, "ynab", "categories", "-eligible")

	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "Groceries")
	assert.Contains(t, r.stdout, "Pets")
	for _, bad := range []string{"Old Stuff", "Inflow", "Visa", "Gone"} {
		assert.NotContains(t, r.stdout, bad)
	}
}

func TestAccounts_listsNameIDTypeAndClosedMarker(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/accounts", ynabtest.OK(map[string]any{"accounts": []map[string]any{
		{"id": "a1", "name": "Walmart Card", "type": "creditCard"},
		{"id": "a2", "name": "Old Checking", "type": "checking", "closed": true},
		{"id": "a3", "name": "Deleted Acct", "type": "checking", "deleted": true},
	}}))

	r := run(t, srv, "ynab", "accounts")

	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "Walmart Card")
	assert.Contains(t, r.stdout, "a1")
	assert.Contains(t, r.stdout, "creditCard")
	assert.Contains(t, r.stdout, "Old Checking")
	assert.Contains(t, r.stdout, "closed")
	assert.NotContains(t, r.stdout, "Deleted Acct")
}

func TestExitCodes(t *testing.T) {
	t.Run("rate limit is 3", func(t *testing.T) {
		srv := ynabtest.New(t)
		srv.On("GET", "/plans/plan-1/accounts", ynabtest.RateLimited())
		r := run(t, srv, "ynab", "accounts")
		assert.Equal(t, 3, r.code)
		assert.Contains(t, r.stderr, "rate limit")
	})
	t.Run("auth failure is 1", func(t *testing.T) {
		srv := ynabtest.New(t)
		srv.On("GET", "/plans/plan-1/accounts", ynabtest.APIError(401, "401", "unauthorized", "Unauthorized"))
		r := run(t, srv, "ynab", "accounts")
		assert.Equal(t, 1, r.code)
	})
	t.Run("missing token env is a config error, 1", func(t *testing.T) {
		cfg := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(cfg, []byte("ynab:\n  token: \"${NOPE_NOT_SET_XYZ}\"\n"), 0o600))
		r := runWithConfig(t, nil, cfg, "ynab", "accounts")
		assert.Equal(t, 1, r.code)
		assert.Contains(t, r.stderr, "NOPE_NOT_SET_XYZ")
	})
	t.Run("missing config file is 1", func(t *testing.T) {
		r := runWithConfig(t, nil, filepath.Join(t.TempDir(), "absent.yaml"), "ynab", "accounts")
		assert.Equal(t, 1, r.code)
	})
	t.Run("unknown command is 1 with usage", func(t *testing.T) {
		r := run(t, nil, "bogus")
		assert.Equal(t, 1, r.code)
		assert.Contains(t, r.stderr, "usage")
	})
	t.Run("no command is 1 with usage", func(t *testing.T) {
		r := run(t, nil)
		assert.Equal(t, 1, r.code)
		assert.Contains(t, r.stderr, "usage")
	})
	t.Run("other failures are 1", func(t *testing.T) {
		srv := ynabtest.New(t)
		srv.On("GET", "/plans/plan-1/accounts", ynabtest.APIError(500, "500", "boom", "boom"))
		assert.Equal(t, 1, run(t, srv, "ynab", "accounts").code)
	})
}

func TestVersion(t *testing.T) {
	r := run(t, nil, "version")

	assert.Equal(t, 0, r.code)
	assert.Contains(t, r.stdout, "itemize-ynab")
}

func TestTokenCannotBePassedAsFlag(t *testing.T) {
	r := run(t, nil, "ynab", "accounts", "-token", "abc")

	assert.Equal(t, 1, r.code)
}

var _ = http.StatusOK

// configExcluding writes a config whose exclude_categories lists names.
func configExcluding(t *testing.T, names ...string) string {
	t.Helper()
	t.Setenv("YNAB_TOKEN", testToken)
	yaml := "ynab:\n  token: \"${YNAB_TOKEN}\"\n  plan_id: plan-1\n  exclude_categories:\n"
	for _, n := range names {
		yaml += "    - " + n + "\n"
	}
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(yaml), 0o600))
	return cfg
}

func TestCategories_eligibleFlagHonorsExcludeCategories(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/categories", categoriesReply())

	r := runWithConfig(t, srv, configExcluding(t, "Pets"), "ynab", "categories", "-eligible")

	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "Groceries")
	assert.NotContains(t, r.stdout, "Pets")
}

func TestCategories_withoutEligibleFlagStillListsExcluded(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/categories", categoriesReply())

	r := runWithConfig(t, srv, configExcluding(t, "Pets"), "ynab", "categories")

	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "Pets")
}
