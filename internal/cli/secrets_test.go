package cli_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

const (
	llmSecret    = "sk-ant-LLM-SECRET-KEY" //nolint:gosec // recognizable fake, not a credential
	cookieSecret = "walmart-COOKIE-SECRET-VALUE"
)

// assertNoSecrets fails when any recognizable secret appears in either stream.
func assertNoSecrets(t *testing.T, r result) {
	t.Helper()
	for _, secret := range []string{wmToken, llmSecret, cookieSecret} {
		assert.NotContains(t, r.stdout, secret, "secret leaked to stdout")
		assert.NotContains(t, r.stderr, secret, "secret leaked to stderr")
	}
}

func TestSecrets_neverPrintedOnAnyWalmartPath(t *testing.T) {
	scenarios := map[string]func(h *wmHarness){
		"normal run, verbose logs": func(h *wmHarness) { h.script() },
		"dry run":                  func(h *wmHarness) { h.script() },
		"failed row": func(h *wmHarness) {
			h.script()
			h.prov.errs["oB"] = errors.New("walmart: order oB: HTTP 500")
		},
		"stopped by YNAB 429": func(h *wmHarness) {
			h.script()
			h.srv.On(http.MethodPut, planPath+"/transactions/tA", ynabtest.RateLimited())
		},
		"YNAB error body echoing the token": func(h *wmHarness) {
			h.script()
			h.srv.On(http.MethodPut, planPath+"/transactions/tA",
				ynabtest.APIError(http.StatusBadRequest, "400", "bad", "rejected token "+wmToken))
		},
		"YNAB rejects the token": func(h *wmHarness) {
			h.script()
			h.srv.On(http.MethodGet, planPath+"/categories",
				ynabtest.APIError(http.StatusUnauthorized, "401", "unauthorized", "bad "+wmToken))
		},
		"config error with keys in the environment": func(h *wmHarness) {
			h.useDefaultChat = true
			h.getenv["ANTHROPIC_API_KEY"] = llmSecret
			h.getenv["CATEGORIZER_PROVIDER"] = "openai" // needs OPENAI_API_KEY, which is absent
		},
		"unusable cookie file holding a secret": func(h *wmHarness) {
			h.useDefaultProvider = true
			h.getenv["ANTHROPIC_API_KEY"] = llmSecret
			assertWrite(t, filepath.Join(h.dir, "cookies.json"), `{"cookies": {"cid": {"value": "`+cookieSecret+`"`)
		},
	}
	for name, setup := range scenarios {
		t.Run(name, func(t *testing.T) {
			h := newWMHarness(t)
			setup(h)
			args := []string{"walmart", "-days", "7", "-verbose"}
			if name == "dry run" {
				args = append(args, "-dry-run")
			}

			r := h.run(args...)

			assertNoSecrets(t, r)
			assert.NotEmpty(t, r.stderr+r.stdout, "the scenario must print something")
		})
	}
}

func assertWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSecrets_failedRowKeepsOnlyShortErrors(t *testing.T) {
	h := newWMHarness(t)
	h.script()
	h.prov.errs["oA"] = &order.BlockedError{Provider: "walmart", Kind: order.StaleSession, Err: errors.New("access denied")}

	r := h.run("walmart", "-days", "7", "-verbose")

	assertNoSecrets(t, r)
	assert.Equal(t, 3, r.code)
}
