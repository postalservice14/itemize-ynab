package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/walmart"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

var quietLog = slog.New(slog.DiscardHandler)

func TestDeps_withDefaultsFillsEveryFactory(t *testing.T) {
	d := Deps{}.withDefaults()

	assert.NotNil(t, d.NewProvider)
	assert.NotNil(t, d.NewChat)
	assert.NotNil(t, d.NewStore)
	assert.NotNil(t, d.Now)
	assert.NotNil(t, d.Sleep)
	assert.WithinDuration(t, time.Now(), d.Now(), time.Minute)
}

func TestDeps_withDefaultsKeepsInjectedFactories(t *testing.T) {
	called := false
	d := Deps{Now: func() time.Time { called = true; return time.Time{} }}.withDefaults()

	_ = d.Now()

	assert.True(t, called)
}

func TestNewWalmartProvider_emptyCookieStoreIsRejectedWithoutNetwork(t *testing.T) {
	cfg := &config.Config{Walmart: config.Walmart{CookieFile: filepath.Join(t.TempDir(), "cookies.json")}}

	_, err := newWalmartProvider(cfg, quietLog)

	require.ErrorIs(t, err, errNoCookies)
	assert.Contains(t, err.Error(), "walmart.cookie_file")
	assert.Contains(t, err.Error(), "import-curl")
}

func TestNewWalmartProvider_importedCookiesBuildAProvider(t *testing.T) {
	cookies := filepath.Join(t.TempDir(), "cookies.json")
	capture := filepath.Join(t.TempDir(), "capture.txt")
	require.NoError(t, os.WriteFile(capture, []byte("curl 'https://x' -b 'CID=a; SPID=b; auth=c'\n"), 0o600))
	client, err := walmart.NewClient(cookies)
	require.NoError(t, err)
	require.NoError(t, client.InitializeFromCurl(capture))

	p, err := newWalmartProvider(&config.Config{Walmart: config.Walmart{CookieFile: cookies}}, quietLog)

	require.NoError(t, err)
	assert.NotNil(t, p)
}

func TestNewChatFromEnv(t *testing.T) {
	_, err := newChatFromEnv(func(string) string { return "" })
	require.Error(t, err)

	chat, err := newChatFromEnv(func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "k"
		}
		return ""
	})
	require.NoError(t, err)
	assert.NotNil(t, chat)
}

func TestOpenStore_createsAndMigratesTheDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iy.db")

	s, err := openStore(context.Background(), path)

	require.NoError(t, err)
	require.NoError(t, s.Close())
	assert.FileExists(t, path)
}
