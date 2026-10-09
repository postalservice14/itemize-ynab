// Package openai is the OpenAI Chat Completions backend for the categorizer's
// ChatClient port. It never retries, and the API key never appears in errors,
// logs or formatted output.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/postalservice14/itemize-ynab/internal/adapters/llm/llmhttp"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
)

const (
	// DefaultBaseURL is the public OpenAI API.
	DefaultBaseURL = "https://api.openai.com"
	provider       = "openai"
	defaultTimeout = 90 * time.Second
)

// Client calls the Chat Completions API.
type Client struct {
	key     llmhttp.Secret
	model   string
	baseURL string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at another server (tests only).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient replaces the HTTP client (and its timeout). The default
// client never follows redirects; a replacement should not either.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// NewClient builds a client for model.
func NewClient(key, model string, opts ...Option) *Client {
	c := &Client{
		key:     llmhttp.Secret(key),
		model:   model,
		baseURL: DefaultBaseURL,
		http:    llmhttp.NewHTTPClient(defaultTimeout),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// String implements fmt.Stringer without exposing the key.
func (c Client) String() string { return fmt.Sprintf("openai.Client{model:%s key:REDACTED}", c.model) }

// GoString implements fmt.GoStringer without exposing the key.
func (c Client) GoString() string { return c.String() }

// Format makes every fmt verb print the redacted form.
func (c Client) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(c.String())) }

// LogValue implements slog.LogValuer without exposing the key.
func (c Client) LogValue() slog.Value { return slog.StringValue(c.String()) }

// MarshalJSON implements json.Marshaler without exposing the key.
func (c Client) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatBody struct {
	Model          string         `json:"model"`
	Messages       []message      `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
	MaxTokens      int            `json:"max_completion_tokens,omitempty"`
}

type chatReply struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Chat implements categorizer.ChatClient.
func (c *Client) Chat(ctx context.Context, req categorizer.ChatRequest) (string, error) {
	body := chatBody{
		Model:          c.model,
		Messages:       []message{{Role: "system", Content: req.System}, {Role: "user", Content: req.User}},
		ResponseFormat: responseFormat{Type: "json_object"},
		MaxTokens:      max(req.MaxTokens, 0),
	}
	headers := map[string]string{"Authorization": "Bearer " + string(c.key)}
	raw, err := llmhttp.Post(ctx, c.http, provider, c.baseURL+"/v1/chat/completions", c.key, headers, body)
	if err != nil {
		return "", err
	}
	var r chatReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", provider, err)
	}
	if len(r.Choices) == 0 || r.Choices[0].Message.Content == "" {
		return "", errors.New(provider + ": response has no message content")
	}
	return r.Choices[0].Message.Content, nil
}
