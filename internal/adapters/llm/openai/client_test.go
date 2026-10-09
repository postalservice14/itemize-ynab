package openai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/llm/openai"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
)

const key = "sk-openai-SECRET"

type captured struct {
	method, path, auth, ctype string
	body                      map[string]any
}

func serve(t *testing.T, status int, reply string) (*httptest.Server, *captured, *atomic.Int32) {
	t.Helper()
	c, hits := &captured{}, &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		c.method, c.path = r.Method, r.URL.Path
		c.auth, c.ctype = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &c.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, c, hits
}

func TestChat_success_requestShapeAndParsing(t *testing.T) {
	srv, c, _ := serve(t, 200, `{"choices":[{"message":{"role":"assistant","content":"{\"items\":[]}"}}]}`)
	cl := openai.NewClient(key, "gpt-test", openai.WithBaseURL(srv.URL))

	got, err := cl.Chat(context.Background(), categorizer.ChatRequest{System: "sys", User: "usr", MaxTokens: 123})

	require.NoError(t, err)
	assert.Equal(t, `{"items":[]}`, got)
	assert.Equal(t, "POST", c.method)
	assert.Equal(t, "/v1/chat/completions", c.path)
	assert.Equal(t, "Bearer "+key, c.auth)
	assert.Equal(t, "application/json", c.ctype)
	assert.Equal(t, "gpt-test", c.body["model"])
	assert.Equal(t, map[string]any{"type": "json_object"}, c.body["response_format"])
	assert.EqualValues(t, 123, c.body["max_completion_tokens"])
	assert.Equal(t, []any{
		map[string]any{"role": "system", "content": "sys"},
		map[string]any{"role": "user", "content": "usr"},
	}, c.body["messages"])
}

func TestChat_errors_typedAndNotRetried(t *testing.T) {
	cases := []struct {
		status int
		is     error
	}{{401, categorizer.ErrAuth}, {403, categorizer.ErrAuth}, {429, categorizer.ErrRateLimited}, {500, categorizer.ErrAPI}}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			srv, _, hits := serve(t, tc.status, `{"error":"x"}`)

			_, err := openai.NewClient(key, "m", openai.WithBaseURL(srv.URL)).Chat(context.Background(), categorizer.ChatRequest{})

			assert.ErrorIs(t, err, tc.is)
			assert.EqualValues(t, 1, hits.Load())
		})
	}
}

func TestChat_malformedOrEmptyBody_error(t *testing.T) {
	for name, body := range map[string]string{
		"not json":   `<html>`,
		"no choices": `{"choices":[]}`,
		"no content": `{"choices":[{"message":{"content":""}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, _, _ := serve(t, 200, body)

			got, err := openai.NewClient(key, "m", openai.WithBaseURL(srv.URL)).Chat(context.Background(), categorizer.ChatRequest{})

			require.Error(t, err)
			assert.Empty(t, got)
		})
	}
}

func TestChat_keyNeverLeaks(t *testing.T) {
	srv, _, _ := serve(t, 401, "bad key "+key)
	cl := openai.NewClient(key, "m", openai.WithBaseURL(srv.URL))
	_, apiErr := cl.Chat(context.Background(), categorizer.ChatRequest{})
	_, netErr := openai.NewClient(key, "m", openai.WithBaseURL("http://127.0.0.1:1/"+key)).Chat(context.Background(), categorizer.ChatRequest{})
	require.Error(t, apiErr)
	require.Error(t, netErr)

	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("c", "client", cl, "c2", *cl, "err", apiErr)
	outputs := []string{buf.String(), apiErr.Error(), netErr.Error(),
		fmt.Sprintf("%v|%+v|%#v|%s", cl, cl, cl, cl),     //nolint:gocritic // verbs under test
		fmt.Sprintf("%v|%+v|%#v|%s", *cl, *cl, *cl, *cl), //nolint:gocritic // verbs under test
		fmt.Sprintf("%v|%+v|%#v", apiErr, netErr, apiErr)}
	for i, out := range outputs {
		assert.NotContains(t, out, key, "output %d", i)
	}
	j, err := json.Marshal(cl)
	require.NoError(t, err)
	assert.NotContains(t, string(j), key)
}

func TestChat_cancelledContext(t *testing.T) {
	srv, _, hits := serve(t, 200, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := openai.NewClient(key, "m", openai.WithBaseURL(srv.URL)).Chat(ctx, categorizer.ChatRequest{})

	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, hits.Load())
}

func TestChat_defaultsAndOptions(t *testing.T) {
	srv, c, _ := serve(t, 200, `{"choices":[{"message":{"content":"ok"}}]}`)
	cl := openai.NewClient(key, "m", openai.WithBaseURL(srv.URL+"/"), openai.WithHTTPClient(srv.Client()))

	_, err := cl.Chat(context.Background(), categorizer.ChatRequest{User: "u"})

	require.NoError(t, err)
	assert.Equal(t, "/v1/chat/completions", c.path, "trailing slash tolerated")
	assert.NotContains(t, c.body, "max_completion_tokens", "omitted when zero")
	assert.Equal(t, "https://api.openai.com", openai.DefaultBaseURL)
}
