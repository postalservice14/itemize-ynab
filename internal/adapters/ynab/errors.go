package ynab

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	// ErrRateLimited is returned (wrapped in an *APIError) when YNAB answers
	// 429. The client never sleeps or retries; callers decide what to do.
	ErrRateLimited = errors.New("ynab: rate limited")

	// ErrUnauthorized is returned (wrapped in an *APIError) for 401 and 403:
	// the token is missing, wrong or lacks access.
	ErrUnauthorized = errors.New("ynab: unauthorized")
)

// APIError is YNAB's error reply: the HTTP status plus the id, name and detail
// from the {"error": {...}} body when one was present.
type APIError struct {
	Method string
	Path   string
	Status int
	ID     string
	Name   string
	Detail string
}

// Error implements error. It never contains the token.
func (e *APIError) Error() string {
	msg := fmt.Sprintf("ynab: %s %s: HTTP %d", e.Method, e.Path, e.Status)
	if e.Name != "" {
		msg += " " + e.Name
	}
	if e.ID != "" {
		msg += " (id " + e.ID + ")"
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Is lets errors.Is match the ErrRateLimited and ErrUnauthorized sentinels.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrRateLimited:
		return e.Status == http.StatusTooManyRequests
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
	default:
		return false
	}
}

// scrubbedError carries a message with secrets already removed. Wrapping keeps
// errors.Is/As working on the cause (for example context.Canceled).
type scrubbedError struct {
	msg   string
	cause error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.cause }
