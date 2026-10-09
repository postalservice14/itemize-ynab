package llmhttp_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/llm/llmhttp"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
)

const key = "sk-test-SECRET-123"

func post(t *testing.T, url string) ([]byte, error) {
	t.Helper()
	return llmhttp.Post(context.Background(), &http.Client{}, "prov", url, llmhttp.Secret(key),
		map[string]string{"X-Key": key}, map[string]string{"a": "b"})
}

func TestPost_success_sendsJSONAndHeaders(t *testing.T) {
	var gotCT, gotKey, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT, gotKey = r.Header.Get("Content-Type"), r.Header.Get("X-Key")
		b := make([]byte, 100)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	body, err := post(t, srv.URL)

	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(body))
	assert.Equal(t, "application/json", gotCT)
	assert.Equal(t, key, gotKey)
	assert.JSONEq(t, `{"a":"b"}`, gotBody)
}

func TestPost_statusMapping_typedErrorsNoRetry(t *testing.T) {
	cases := []struct {
		status int
		is     error
		not    error
	}{
		{401, categorizer.ErrAuth, categorizer.ErrRateLimited},
		{403, categorizer.ErrAuth, categorizer.ErrRateLimited},
		{429, categorizer.ErrRateLimited, categorizer.ErrAuth},
		{500, categorizer.ErrAPI, categorizer.ErrAuth},
		{418, categorizer.ErrAPI, categorizer.ErrRateLimited},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("nope: " + key))
			}))
			defer srv.Close()

			_, err := post(t, srv.URL)

			require.Error(t, err)
			assert.ErrorIs(t, err, tc.is)
			assert.NotErrorIs(t, err, tc.not)
			var apiErr *categorizer.APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tc.status, apiErr.Status)
			assert.NotContains(t, err.Error(), key)
			assert.Equal(t, int32(1), hits.Load(), "no retry")
		})
	}
}

func TestPost_longMultibyteBody_truncatedRuneSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(strings.Repeat("é世🙂", 5000)))
	}))
	defer srv.Close()

	_, err := post(t, srv.URL)

	var apiErr *categorizer.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.True(t, utf8.ValidString(apiErr.Snippet))
	assert.LessOrEqual(t, len(apiErr.Snippet), 400)
	assert.Less(t, len(err.Error()), 600)
}

func TestPost_oversizedSuccessBody_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", llmhttp.MaxBody+10)))
	}))
	defer srv.Close()

	_, err := post(t, srv.URL)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
}

func TestPost_transportError_scrubbedAndKeepsCause(t *testing.T) {
	_, err := post(t, "http://127.0.0.1:1/"+key)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), key)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = llmhttp.Post(ctx, &http.Client{}, "prov", "http://127.0.0.1:1/"+key, llmhttp.Secret(key), nil, 1)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.NotContains(t, err.Error(), key)
}

func TestPost_unmarshalableBody_error(t *testing.T) {
	_, err := llmhttp.Post(context.Background(), &http.Client{}, "prov", "http://x", llmhttp.Secret(key), nil, make(chan int))
	require.Error(t, err)
}

func TestSecret_neverPrints(t *testing.T) {
	s := llmhttp.Secret(key)
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "s", s)
	for _, out := range []string{
		fmt.Sprintf("%v", s), fmt.Sprintf("%+v", s), fmt.Sprintf("%#v", s), fmt.Sprintf("%s", s), buf.String(), //nolint:gocritic // verbs under test
	} {
		assert.NotContains(t, out, key)
	}
	b, err := s.MarshalText()
	require.NoError(t, err)
	assert.NotContains(t, string(b), key)
}

func TestNewHTTPClient_timeoutAndNoRedirects(t *testing.T) {
	c := llmhttp.NewHTTPClient(7 * time.Second)

	assert.Equal(t, 7*time.Second, c.Timeout)
	require.NotNil(t, c.CheckRedirect)
	assert.ErrorIs(t, c.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}
