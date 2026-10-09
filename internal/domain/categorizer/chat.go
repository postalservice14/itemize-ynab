// Package categorizer assigns each order item to one YNAB category using an
// LLM, reached only through the ChatClient port. It is pure: no HTTP, storage
// or clock. The model's answer is untrusted; every answer is validated against
// the set of acceptable categories and the categorizer never invents a default
// category when the model fails to give a valid one.
package categorizer

import (
	"context"
	"errors"
	"fmt"
)

// ChatRequest is one provider-neutral, single-turn LLM call.
type ChatRequest struct {
	// System holds the fixed instructions. It never contains item text or
	// secrets.
	System string
	// User holds the task data.
	User string
	// MaxTokens bounds the reply length.
	MaxTokens int
}

// ChatClient is the port the categorizer needs: send one request, get the
// model's text reply. Implementations return errors matching ErrAuth,
// ErrRateLimited or *APIError (via errors.Is/As) and must not retry.
type ChatClient interface {
	Chat(ctx context.Context, req ChatRequest) (string, error)
}

var (
	// ErrAuth means the LLM provider rejected the credentials (HTTP 401/403).
	ErrAuth = errors.New("categorizer: LLM provider rejected the credentials")
	// ErrRateLimited means the LLM provider throttled the request (HTTP 429).
	// Callers decide whether to stop or wait; clients never retry.
	ErrRateLimited = errors.New("categorizer: LLM provider rate limited the request")
	// ErrAPI matches any *APIError.
	ErrAPI = errors.New("categorizer: LLM provider API error")
)

// APIError is a non-success reply from an LLM provider. Snippet is a truncated
// piece of the body and never contains the API key.
type APIError struct {
	Provider string
	Status   int
	Snippet  string
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Snippet == "" {
		return fmt.Sprintf("%s: HTTP %d", e.Provider, e.Status)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, e.Snippet)
}

// Is matches ErrAPI for every status, ErrAuth for 401/403 and ErrRateLimited
// for 429.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrAPI:
		return true
	case ErrAuth:
		return e.Status == 401 || e.Status == 403
	case ErrRateLimited:
		return e.Status == 429
	default:
		return false
	}
}
