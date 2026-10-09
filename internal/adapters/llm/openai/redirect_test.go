package openai_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/llm/openai"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
)

func TestChat_redirect_isNotFollowedAndTheKeyNeverLeaves(t *testing.T) {
	var hits atomic.Int32
	var gotKey atomic.Value
	gotKey.Store("")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotKey.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"}}]}`))
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	_, err := openai.NewClient(key, "m", openai.WithBaseURL(redirector.URL)).
		Chat(context.Background(), categorizer.ChatRequest{System: "s", User: "u"})

	assert.Zero(t, hits.Load(), "the redirect target must never be called")
	assert.Empty(t, gotKey.Load(), "the key must never reach another host")
	var apiErr *categorizer.APIError
	require.True(t, errors.As(err, &apiErr), "the redirect is an API error, got %v", err)
	assert.Equal(t, http.StatusFound, apiErr.Status)
	assert.NotContains(t, err.Error(), key)
}
