package order

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestChargeKey(t *testing.T) {
	assert.Equal(t, "walmart:200012345:4500:1", ChargeKey("walmart", "200012345", 4500, 1))
	assert.Equal(t, "walmart:200012345:4500:2", ChargeKey("walmart", "200012345", 4500, 2))
}

func TestResolveAccount(t *testing.T) {
	m := map[string]string{"0953": "acct-1", "1111": ""}
	id, ok := ResolveAccount("0953", m)
	assert.True(t, ok)
	assert.Equal(t, "acct-1", id)

	_, ok = ResolveAccount("9999", m)
	assert.False(t, ok)

	_, ok = ResolveAccount("", m)
	assert.False(t, ok, "empty last four never resolves")

	_, ok = ResolveAccount("1111", m)
	assert.False(t, ok, "empty account id is not a resolution")

	_, ok = ResolveAccount("0953", nil)
	assert.False(t, ok)
}

func TestDateOnly(t *testing.T) {
	loc := time.FixedZone("EDT", -4*3600)
	late := time.Date(2026, 3, 5, 23, 30, 0, 0, loc) // 03:30 UTC on the 6th
	got := DateOnly(late)
	assert.Equal(t, time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), got)
	assert.Equal(t, time.UTC, got.Location())

	assert.True(t, DateOnly(time.Time{}).IsZero())
}

func TestBlockedError(t *testing.T) {
	cause := errors.New("boom")
	err := &BlockedError{Provider: "walmart", Kind: BotChallenge, Err: cause}
	assert.ErrorIs(t, err, ErrBlocked)
	assert.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "walmart")
	assert.Contains(t, err.Error(), "bot challenge")

	var be *BlockedError
	assert.True(t, errors.As(error(err), &be))
	assert.Equal(t, BotChallenge, be.Kind)

	assert.False(t, errors.Is(cause, ErrBlocked))
	for _, k := range []BlockedKind{BotChallenge, StaleSession, RateLimited, BlockedKind(99)} {
		assert.NotEmpty(t, k.String())
	}
}

func TestSkipReasonString(t *testing.T) {
	for _, r := range []SkipReason{GiftCard, NonCardPayment, Refund, ZeroAmount, SkipReason(99)} {
		assert.NotEmpty(t, r.String())
	}
	assert.Equal(t, "refund", Refund.String())
}

func TestBlockedError_noCause(t *testing.T) {
	err := &BlockedError{Provider: "walmart", Kind: RateLimited}
	assert.Equal(t, "walmart: blocked (rate limited)", err.Error())
	assert.ErrorIs(t, err, ErrBlocked)
}
