package cli_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	curlCookieA = "CID-cookie-SECRET-aaa"
	curlCookieB = "SPID-cookie-SECRET-bbb"
	curlCookieC = "auth-cookie-SECRET-ccc"
)

// syntheticCurl is a made-up browser capture; no value is a real credential.
func syntheticCurl() string {
	return "curl 'https://www.walmart.com/orchestra/orders/graphql/getOrder/" + strings.Repeat("a", 64) + "' \\\n" +
		"  -H 'accept: application/json' \\\n" +
		"  -b 'CID=" + curlCookieA + "; SPID=" + curlCookieB + "; auth=" + curlCookieC + "'\n"
}

func assertNoCookieValues(t *testing.T, r result) {
	t.Helper()
	for _, v := range []string{curlCookieA, curlCookieB, curlCookieC} {
		assert.NotContains(t, r.stdout, v)
		assert.NotContains(t, r.stderr, v)
	}
}

func TestImportCurl_fromFileWritesPrivateCookieStore(t *testing.T) {
	h := newWMHarness(t)
	capture := filepath.Join(h.dir, "capture.txt")
	require.NoError(t, os.WriteFile(capture, []byte(syntheticCurl()), 0o600))
	cookies := filepath.Join(h.dir, "nested", "cookies.json")
	h.writeConfig("")
	h.cfg = rewriteCookiePath(t, h, cookies)

	r := h.run("walmart", "import-curl", capture)

	require.Equal(t, 0, r.code, r.stderr)
	assert.Equal(t, "cookie store written to "+cookies+"\n", r.stdout)
	assertNoCookieValues(t, r)
	info, err := os.Stat(cookies)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	data, err := os.ReadFile(cookies) //nolint:gosec // a path inside t.TempDir()
	require.NoError(t, err)
	assert.Contains(t, string(data), curlCookieA, "the cookie store itself does hold the values")
	h.untouched()
}

func TestImportCurl_fromStdinLeavesNoTempFile(t *testing.T) {
	h := newWMHarness(t)
	h.stdin = syntheticCurl()

	r := h.run("walmart", "import-curl", "-")

	require.Equal(t, 0, r.code, r.stderr)
	assertNoCookieValues(t, r)
	assert.FileExists(t, filepath.Join(h.dir, "cookies.json"))
	leftovers, err := filepath.Glob(filepath.Join(h.dir, ".curl-*"))
	require.NoError(t, err)
	assert.Empty(t, leftovers)
}

func TestImportCurl_failuresExitOneWithoutEchoingTheCapture(t *testing.T) {
	tests := map[string]struct {
		stdin string
		args  []string
		want  string
	}{
		"no cookies in the capture": {"curl 'https://example.com' -H 'a: b'\n", []string{"-"}, "no cookies"},
		"missing required cookie":   {"curl 'https://x' -b 'CID=" + curlCookieA + "'\n", []string{"-"}, "required"},
		"unreadable file":           {"", []string{"does-not-exist.txt"}, "import the cURL capture"},
		"no argument":               {"", nil, "usage"},
		"two arguments":             {"", []string{"a", "b"}, "usage"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := newWMHarness(t)
			h.stdin = tc.stdin

			r := h.run(append([]string{"walmart", "import-curl"}, tc.args...)...)

			assert.Equal(t, 1, r.code)
			assert.Contains(t, r.stderr, tc.want)
			assertNoCookieValues(t, r)
			assert.NoFileExists(t, filepath.Join(h.dir, "cookies.json"))
		})
	}
}

func TestImportCurl_rejectsWalmartFlags(t *testing.T) {
	h := newWMHarness(t)

	r := h.run("walmart", "import-curl", "-dry-run", "x.txt")

	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "takes no flags")
}

func TestImportCurl_noStdinGivenIsAnError(t *testing.T) {
	h := newWMHarness(t)
	h.stdin = ""

	r := h.run("walmart", "import-curl", "-")

	assert.Equal(t, 1, r.code)
	assertNoCookieValues(t, r)
}

func rewriteCookiePath(t *testing.T, h *wmHarness, cookies string) string {
	t.Helper()
	data, err := os.ReadFile(h.cfg)
	require.NoError(t, err)
	out := strings.ReplaceAll(string(data), filepath.Join(h.dir, "cookies.json"), cookies)
	require.NoError(t, os.WriteFile(h.cfg, []byte(out), 0o600)) //nolint:gosec // a path inside t.TempDir()
	return h.cfg
}

func TestImportCurl_runsWithoutTheYNABToken(t *testing.T) {
	h := newWMHarness(t)
	t.Setenv("YNAB_TOKEN", "")
	h.stdin = syntheticCurl()

	r := h.run("walmart", "import-curl", "-")

	require.Equal(t, 0, r.code, r.stderr)
	assert.FileExists(t, filepath.Join(h.dir, "cookies.json"))
}
