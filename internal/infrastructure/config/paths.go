package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	defaultDatabasePath   = "itemize-ynab.db"
	defaultCookieFilePath = "~/.walmart-api/cookies.json"
)

// pathResolver turns the configured file paths into usable ones. A nil
// resolver keeps them as written (defaults filled in).
type pathResolver struct {
	baseDir string
	homeDir string
}

// apply sets cfg.Database.Path and cfg.Walmart.CookieFile from the expanded
// raw values, applying defaults first.
func (r *pathResolver) apply(cfg *Config, dbPath, cookieFile string) error {
	if dbPath == "" {
		dbPath = defaultDatabasePath
	}
	if cookieFile == "" {
		cookieFile = defaultCookieFilePath
	}
	if r == nil {
		cfg.Database.Path, cfg.Walmart.CookieFile = dbPath, cookieFile
		return nil
	}
	var err error
	if cfg.Database.Path, err = r.resolve("database.path", dbPath); err != nil {
		return err
	}
	cfg.Walmart.CookieFile, err = r.resolve("walmart.cookie_file", cookieFile)
	return err
}

func (r *pathResolver) resolve(setting, p string) (string, error) {
	if rest, isHome := strings.CutPrefix(p, "~"); isHome {
		if rest != "" && rest[0] != '/' && rest[0] != filepath.Separator {
			return "", fmt.Errorf("%w: %s: only a leading ~ for the home directory is supported", ErrInvalid, setting)
		}
		if r.homeDir == "" {
			return "", fmt.Errorf("%w: %s starts with ~ but the home directory is unknown", ErrInvalid, setting)
		}
		return filepath.Join(r.homeDir, rest), nil
	}
	if filepath.IsAbs(p) || r.baseDir == "" {
		return filepath.Clean(p), nil
	}
	return filepath.Join(r.baseDir, p), nil
}
