package walmart

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	wm "github.com/eshaffer321/walmart-client-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantKind order.BlockedKind
		blocked  bool
	}{
		{"bot challenge sentinel", fmt.Errorf("%w: HTTP 456; refresh", wm.ErrBotChallenge), order.BotChallenge, true},
		{"403/418 order", errors.New("access denied - cookies expired, please update from browser"), order.StaleSession, true},
		{"403/418 ledger", errors.New("access denied (cookies might be stale) - try refreshing from browser"), order.StaleSession, true},
		{"429 order", errors.New("rate limited - cookies might be stale, try refreshing from browser"), order.RateLimited, true},
		{"429 ledger exhausted", errors.New("after 3 retries: rate limited (attempt 4/4)"), order.RateLimited, true},
		{"wrapped by autodetect", fmt.Errorf("order not found as either in-store or delivery: %w", errors.New("access denied - cookies expired")), order.StaleSession, true},
		{"perimeterx 412 block", errors.New(`list purchase history: HTTP 412: {"redirectUrl":"/blocked?url=L29yZGVycw==&uuid=u&vid=v&g=b","appId":"PXu6b0qd2S","jsClientSrc":"/px/PXu6b0qd2S/init.js"}`), order.BotChallenge, true},
		{"412 without a block redirect", errors.New(`HTTP 412: {"error":"precondition failed"}`), 0, false},
		{"block redirect on another status", errors.New(`HTTP 500: {"redirectUrl":"/blocked?url=x"}`), 0, false},
		{"other", errors.New("unexpected status code: 500"), 0, false},
		{"http body mentioning rate limited", errors.New("HTTP 500: {\"msg\":\"rate limited upstream\"}"), 0, false},
		{"cancel while rate limiting", fmt.Errorf("request canceled while rate limiting: %w", context.Canceled), 0, false},
		{"nil", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, ok := classifyError(tt.err)
			assert.Equal(t, tt.blocked, ok)
			if ok {
				assert.Equal(t, tt.wantKind, kind)
			}
		})
	}
}

func TestWrapError_blocked(t *testing.T) {
	err := wrapError("W1", "get order", fmt.Errorf("%w: HTTP 456", wm.ErrBotChallenge))
	require.ErrorIs(t, err, order.ErrBlocked)
	require.ErrorIs(t, err, wm.ErrBotChallenge)
	var be *order.BlockedError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, order.BotChallenge, be.Kind)
	assert.Equal(t, "walmart", be.Provider)
}

func TestWrapError_other(t *testing.T) {
	err := wrapError("W1", "get ledger", errors.New("unexpected status code: 500"))
	assert.NotErrorIs(t, err, order.ErrBlocked)
	assert.Contains(t, err.Error(), "W1")
	assert.Contains(t, err.Error(), "get ledger")

	ctxErr := wrapError("W1", "get order", fmt.Errorf("request canceled: %w", context.Canceled))
	assert.ErrorIs(t, ctxErr, context.Canceled)
}

func TestWrapError_truncatesLongBodies(t *testing.T) {
	err := wrapError("W1", "get order", errors.New("HTTP 500: "+strings.Repeat("x", 5000)))
	assert.Less(t, len(err.Error()), 400)
}
