// Package llmhttp is the HTTP plumbing shared by the LLM backends: a redacting
// secret type and a single bounded JSON POST that maps failures to the typed
// errors of the categorizer package. It never retries or sleeps.
package llmhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
)

const (
	// MaxBody bounds how much of a response body is read.
	MaxBody = 4 << 20
	// snippetBytes bounds the body excerpt placed in an error.
	snippetBytes = 300
	redacted     = "REDACTED"
)

// Secret holds an API key. Every way of printing it yields a redaction marker.
type Secret string

func (Secret) String() string               { return redacted }
func (Secret) GoString() string             { return redacted }
func (Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Secret) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(redacted)) }

// scrub removes the key from text.
func (s Secret) scrub(text string) string {
	if s == "" {
		return text
	}
	return strings.ReplaceAll(text, string(s), redacted)
}

// scrubbedError carries an already-scrubbed message and keeps the cause
// reachable for errors.Is/As (for example context.Canceled).
type scrubbedError struct {
	msg   string
	cause error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.cause }

// NewHTTPClient returns an HTTP client with the given timeout that never
// follows redirects: the backends send their key in a header, and Go forwards
// custom headers to a redirect target on another host. A redirect reply is
// returned as is, so Post reports it as an API error.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Post sends body as JSON to url with the given headers and returns the
// response body of a 2xx reply. Non-2xx replies become *categorizer.APIError;
// transport failures keep their cause but never show key.
func Post(ctx context.Context, hc *http.Client, provider, url string, key Secret, headers map[string]string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", provider, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, &scrubbedError{msg: key.scrub(fmt.Sprintf("%s: build request: %v", provider, err)), cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &scrubbedError{msg: key.scrub(fmt.Sprintf("%s: request failed: %v", provider, err)), cause: err}
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return nil, &scrubbedError{msg: key.scrub(fmt.Sprintf("%s: read response: %v", provider, err)), cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &categorizer.APIError{Provider: provider, Status: resp.StatusCode, Snippet: snippet(key.scrub(string(data)))}
	}
	if len(data) > MaxBody {
		return nil, errors.New(provider + ": response too large")
	}
	return data, nil
}

// snippet trims s and cuts it to snippetBytes on a rune boundary.
func snippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= snippetBytes {
		return strings.ToValidUTF8(s, "?")
	}
	cut := snippetBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.ToValidUTF8(s[:cut], "?") + "..."
}
