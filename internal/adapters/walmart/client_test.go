package walmart

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient_constructsWithoutNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	c, err := NewClient(path, WithMaxRetries(-1), WithRateLimits(time.Second, 5*time.Second))
	require.NoError(t, err)
	assert.NotNil(t, c)
	assert.Equal(t, 0, c.CookieCount(), "missing cookie file is a valid empty store")
}

func TestClientConfig_defaults(t *testing.T) {
	cfg := newClientConfig("p")
	assert.Equal(t, "p", cfg.CookieFile)
	assert.Equal(t, 0, cfg.MaxRetries, "0 keeps the client's built-in 3 retries (5s/10s/20s)")

	cfg = newClientConfig("p", WithMaxRetries(-1))
	assert.Equal(t, -1, cfg.MaxRetries)
}
