package anthropic_test

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

	"github.com/postalservice14/itemize-ynab/internal/adapters/llm/anthropic"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
)

const key = "sk-ant-SECRET"

type captured struct {
	method, path, apiKey, version, ctype string
	body                                 map[string]any
}

func serve(t *testing.T, status int, reply string) (*httptest.Server, *captured, *atomic.Int32) {
	t.Helper()
	c, hits := &captured{}, &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		c.method, c.path = r.Method, r.URL.Path
		c.apiKey, c.version, c.ctype = r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &c.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, c, hits
}

func TestChat_success_requestShapeAndParsing(t *testing.T) {
	srv, c, _ := serve(t, 200, `{"content":[{"type":"text","text":"{\"items\":"},{"type":"tool_use"},{"type":"text","text":"[]}"}],"stop_reason":"end_turn"}`)
	cl := anthropic.NewClient(key, "claude-test", anthropic.WithBaseURL(srv.URL))

	got, err := cl.Chat(context.Background(), categorizer.ChatRequest{System: "sys", User: "usr", MaxTokens: 123})

	require.NoError(t, err)
	assert.Equal(t, `{"items":[]}`, got)
	assert.Equal(t, "POST", c.method)
	assert.Equal(t, "/v1/messages", c.path)
	assert.Equal(t, key, c.apiKey)
	assert.Equal(t, "2023-06-01", c.version)
	assert.Equal(t, "application/json", c.ctype)
	assert.Equal(t, "claude-test", c.body["model"])
	assert.EqualValues(t, 123, c.body["max_tokens"])
	assert.Equal(t, "sys", c.body["system"])
	assert.Equal(t, []any{map[string]any{"role": "user", "content": "usr"}}, c.body["messages"])
}

func TestChat_errors_typedAndNotRetried(t *testing.T) {
	cases := []struct {
		status int
		is     error
	}{{401, categorizer.ErrAuth}, {403, categorizer.ErrAuth}, {429, categorizer.ErrRateLimited}, {500, categorizer.ErrAPI}}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			srv, _, hits := serve(t, tc.status, `{"error":"x"}`)

			_, err := anthropic.NewClient(key, "m", anthropic.WithBaseURL(srv.URL)).Chat(context.Background(), categorizer.ChatRequest{})

			assert.ErrorIs(t, err, tc.is)
			assert.EqualValues(t, 1, hits.Load())
		})
	}
}

func TestChat_malformedOrEmptyBody_error(t *testing.T) {
	for name, body := range map[string]string{
		"not json":   `<html>`,
		"no content": `{"content":[]}`,
		"only tool":  `{"content":[{"type":"tool_use"}]}`,
		"empty text": `{"content":[{"type":"text","text":""}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, _, _ := serve(t, 200, body)

			got, err := anthropic.NewClient(key, "m", anthropic.WithBaseURL(srv.URL)).Chat(context.Background(), categorizer.ChatRequest{})

			require.Error(t, err)
			assert.Empty(t, got)
		})
	}
}

func TestChat_keyNeverLeaks(t *testing.T) {
	srv, _, _ := serve(t, 401, "bad key "+key)
	cl := anthropic.NewClient(key, "m", anthropic.WithBaseURL(srv.URL))
	_, apiErr := cl.Chat(context.Background(), categorizer.ChatRequest{})
	_, netErr := anthropic.NewClient(key, "m", anthropic.WithBaseURL("http://127.0.0.1:1/"+key)).Chat(context.Background(), categorizer.ChatRequest{})
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

	_, err := anthropic.NewClient(key, "m", anthropic.WithBaseURL(srv.URL)).Chat(ctx, categorizer.ChatRequest{})

	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, hits.Load())
}

func TestChat_defaultsAndOptions(t *testing.T) {
	srv, c, _ := serve(t, 200, `{"content":[{"type":"text","text":"ok"}]}`)
	cl := anthropic.NewClient(key, "m", anthropic.WithBaseURL(srv.URL+"/"), anthropic.WithHTTPClient(srv.Client()))

	_, err := cl.Chat(context.Background(), categorizer.ChatRequest{User: "u"})

	require.NoError(t, err)
	assert.Equal(t, "/v1/messages", c.path, "trailing slash tolerated")
	assert.NotContains(t, c.body, "max_completion_tokens", "omitted when zero")
	assert.Equal(t, "https://api.anthropic.com", anthropic.DefaultBaseURL)
}
