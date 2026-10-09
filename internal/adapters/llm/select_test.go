package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/llm"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestNewChatClientFromEnv_selection(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		wantName string
		wantErr  string
	}{
		{"anthropic only", map[string]string{"ANTHROPIC_API_KEY": "a"}, "anthropic", ""},
		{"anthropic alias", map[string]string{"CLAUDE_API_KEY": "a"}, "anthropic", ""},
		{"openai only", map[string]string{"OPENAI_API_KEY": "o", "OPENAI_MODEL": "m"}, "openai", ""},
		{"openai alias", map[string]string{"OPENAI_APIKEY": "o", "OPENAI_MODEL": "m"}, "openai", ""},
		{"primary beats alias", map[string]string{"OPENAI_API_KEY": "o", "OPENAI_APIKEY": "x", "OPENAI_MODEL": "m"}, "openai", ""},
		{"both keys prefers anthropic", map[string]string{"ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "o"}, "anthropic", ""},
		{"both keys forced openai", map[string]string{"ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "o", "OPENAI_MODEL": "m", "CATEGORIZER_PROVIDER": "openai"}, "openai", ""},
		{"both keys forced anthropic", map[string]string{"ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "o", "CATEGORIZER_PROVIDER": " Anthropic "}, "anthropic", ""},
		{"openai without model", map[string]string{"OPENAI_API_KEY": "o"}, "", "OPENAI_MODEL"},
		{"both keys prefers anthropic even without openai model", map[string]string{"ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "o"}, "anthropic", ""},
		{"no key", map[string]string{}, "", "ANTHROPIC_API_KEY"},
		{"blank keys ignored", map[string]string{"ANTHROPIC_API_KEY": "  ", "OPENAI_API_KEY": ""}, "", "no LLM API key"},
		{"unknown provider", map[string]string{"ANTHROPIC_API_KEY": "a", "CATEGORIZER_PROVIDER": "gemini"}, "", "gemini"},
		{"forced provider lacks key", map[string]string{"ANTHROPIC_API_KEY": "a", "CATEGORIZER_PROVIDER": "openai"}, "", "OPENAI_API_KEY"},
		{"forced anthropic lacks key", map[string]string{"OPENAI_API_KEY": "o", "CATEGORIZER_PROVIDER": "anthropic"}, "", "ANTHROPIC_API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, name, err := llm.NewChatClientFromEnv(env(tc.env))

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.ErrorIs(t, err, llm.ErrConfig)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, client)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantName, name)
			assert.NotNil(t, client)
		})
	}
}

func TestNewChatClientFromEnv_errorsNeverContainKeys(t *testing.T) {
	_, _, err := llm.NewChatClientFromEnv(env(map[string]string{"OPENAI_API_KEY": "sk-SECRET", "CATEGORIZER_PROVIDER": "bogus"}))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sk-SECRET")
	_, _, err = llm.NewChatClientFromEnv(env(map[string]string{"OPENAI_API_KEY": "sk-SECRET"}))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sk-SECRET")
}

func TestNewChatClientFromEnv_modelsAndBaseURLsAreHonored(t *testing.T) {
	var gotPath, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hi"}],"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	cases := []struct {
		env       map[string]string
		wantPath  string
		wantModel string
	}{
		{map[string]string{"ANTHROPIC_API_KEY": "a", "ANTHROPIC_BASE_URL": srv.URL}, "/v1/messages", llm.DefaultAnthropicModel},
		{map[string]string{"ANTHROPIC_API_KEY": "a", "ANTHROPIC_BASE_URL": srv.URL, "ANTHROPIC_MODEL": "claude-x"}, "/v1/messages", "claude-x"},
		{map[string]string{"OPENAI_API_KEY": "o", "OPENAI_MODEL": "gpt-x", "OPENAI_BASE_URL": srv.URL}, "/v1/chat/completions", "gpt-x"},
	}
	for _, tc := range cases {
		client, _, err := llm.NewChatClientFromEnv(env(tc.env))
		require.NoError(t, err)

		out, err := client.Chat(context.Background(), categorizer.ChatRequest{User: "u"})

		require.NoError(t, err)
		assert.Equal(t, "hi", out)
		assert.Equal(t, tc.wantPath, gotPath)
		assert.Equal(t, tc.wantModel, gotModel)
	}
	assert.Equal(t, "claude-haiku-5-5", llm.DefaultAnthropicModel)
}
