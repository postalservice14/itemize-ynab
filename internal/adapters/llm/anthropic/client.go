// Package anthropic is the Anthropic Messages API backend for the
// categorizer's ChatClient port. It never retries, and the API key never
// appears in errors, logs or formatted output.
package anthropic

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
	// DefaultBaseURL is the public Anthropic API.
	DefaultBaseURL = "https://api.anthropic.com"
	// apiVersion is the Messages API version header value.
	apiVersion       = "2023-06-01"
	provider         = "anthropic"
	defaultTimeout   = 90 * time.Second
	defaultMaxTokens = 1024
)

// Client calls the Messages API.
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
func (c Client) String() string {
	return fmt.Sprintf("anthropic.Client{model:%s key:REDACTED}", c.model)
}

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

type messagesBody struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []message `json:"messages"`
}

type messagesReply struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// Chat implements categorizer.ChatClient. The reply is the concatenation of
// the response's text blocks.
func (c *Client) Chat(ctx context.Context, req categorizer.ChatRequest) (string, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	body := messagesBody{
		Model:     c.model,
		MaxTokens: maxTokens,
		System:    req.System,
		Messages:  []message{{Role: "user", Content: req.User}},
	}
	headers := map[string]string{"x-api-key": string(c.key), "anthropic-version": apiVersion}
	raw, err := llmhttp.Post(ctx, c.http, provider, c.baseURL+"/v1/messages", c.key, headers, body)
	if err != nil {
		return "", err
	}
	var r messagesReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", provider, err)
	}
	var text strings.Builder
	for _, b := range r.Content {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	if text.Len() == 0 {
		return "", errors.New(provider + ": response has no text content")
	}
	return text.String(), nil
}
