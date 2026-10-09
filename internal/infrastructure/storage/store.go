// Package storage is the SQLite store: the idempotency record of processed
// charges and a local copy of YNAB transactions with the server_knowledge that
// describes it. Transactions are stored as opaque JSON; this package never
// interprets them.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const (
	memoryPath  = ":memory:"
	timeLayout  = time.RFC3339Nano
	busyTimeout = "busy_timeout(5000)"
)

// Store is a SQLite-backed store. It is safe for concurrent use.
type Store struct {
	db    *sql.DB
	clock func() time.Time
}

// Option configures Open.
type Option func(*Store)

// WithClock sets the clock used for bookkeeping timestamps (updated_at).
// The default is time.Now.
func WithClock(clock func() time.Time) Option {
	return func(s *Store) { s.clock = clock }
}

// Open opens (creating if needed) the database at path, applies the embedded
// migrations and returns the store. Use ":memory:" for a private in-memory
// database.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("storage: open %q: %w", path, err)
	}
	if path == memoryPath {
		// Every connection to ":memory:" is its own database, so share one.
		db.SetMaxOpenConns(1)
	}
	s := &Store{db: db, clock: time.Now}
	for _, o := range opts {
		o(s)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: connect %q: %w", path, err)
	}
	if err := migrateUp(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func dsn(path string) string {
	pragmas := []string{"foreign_keys(1)", busyTimeout}
	name := path
	if path == memoryPath {
		name = "file::memory:"
	} else {
		pragmas = append(pragmas, "journal_mode(WAL)")
		if !strings.HasPrefix(path, "file:") {
			name = "file:" + path
		}
	}
	sep := "?"
	if strings.Contains(name, "?") {
		sep = "&"
	}
	var b strings.Builder
	b.WriteString(name)
	for i, p := range pragmas {
		if i == 0 {
			b.WriteString(sep)
		} else {
			b.WriteString("&")
		}
		b.WriteString("_pragma=" + p)
	}
	return b.String()
}

func newProvider(db *sql.DB) (*goose.Provider, error) {
	sub, err := fs.Sub(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("storage: migrations fs: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return nil, fmt.Errorf("storage: migration provider: %w", err)
	}
	return p, nil
}

func migrateUp(ctx context.Context, db *sql.DB) error {
	p, err := newProvider(db)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("storage: migrate up: %w", err)
	}
	return nil
}

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }
