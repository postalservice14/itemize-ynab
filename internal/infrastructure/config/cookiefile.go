package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// LoadCookieFile reads path and returns only walmart.cookie_file, resolved the
// way Load resolves it. Nothing outside the `walmart:` section is expanded or
// validated, so `walmart import-curl` runs without the YNAB token.
func LoadCookieFile(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the config path is an operator-supplied argument
	if err != nil {
		return "", fmt.Errorf("%w: read %s: %w", ErrInvalid, path, err)
	}
	home, _ := os.UserHomeDir() // an unknown home only matters if a path starts with ~
	return ParseCookieFile(data, os.LookupEnv, filepath.Dir(path), home)
}

// ParseCookieFile is LoadCookieFile on already-read YAML: the `walmart:`
// section is decoded strictly and its ${VAR} references expanded with lookup;
// every other section is ignored.
func ParseCookieFile(data []byte, lookup func(string) (string, bool), baseDir, homeDir string) (string, error) {
	top, err := decodeTop(data)
	if err != nil {
		return "", err
	}
	raw, err := decodeSection[rawWalmart](top, "walmart", false)
	if err != nil {
		return "", err
	}
	exp := &expander{lookup: lookup, missing: map[string]struct{}{}}
	cookieFile := exp.expand(raw.CookieFile)
	if err := exp.err(); err != nil {
		return "", err
	}
	if cookieFile == "" {
		cookieFile = defaultCookieFilePath
	}
	res := &pathResolver{baseDir: baseDir, homeDir: homeDir}
	return res.resolve("walmart.cookie_file", cookieFile)
}
