package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/postalservice14/itemize-ynab/internal/adapters/walmart"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

// errNoCookies marks an empty or missing Walmart cookie store.
var errNoCookies = errors.New("walmart cookie store is empty")

// maxCurlBytes bounds what is read from stdin; a browser capture is a few KB.
const maxCurlBytes = 4 << 20

// runImportCurl writes the Walmart cookie store at walmart.cookie_file from a
// browser "Copy as cURL" capture, read from src (a file, or "-" for stdin). It
// makes no network call. Cookie values and the capture text are never printed.
func runImportCurl(configPath, src string, env Env) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	dest := cfg.Walmart.CookieFile
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("create the directory for walmart.cookie_file: %w", err)
	}
	captureFile := src
	if src == "-" {
		tmp, cleanup, err := stdinToTempFile(env.Stdin, filepath.Dir(dest))
		if err != nil {
			return err
		}
		defer cleanup()
		captureFile = tmp
	}
	client, err := walmart.NewClient(dest)
	if err != nil {
		return fmt.Errorf("create the Walmart client (setting walmart.cookie_file): %w", err)
	}
	if err := client.InitializeFromCurl(captureFile); err != nil {
		return fmt.Errorf("import the cURL capture: %w", err)
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return fmt.Errorf("restrict the cookie file's permissions: %w", err)
	}
	_, _ = fmt.Fprintf(env.Stdout, "cookie store written to %s\n", dest)
	return nil
}

// stdinToTempFile copies stdin into a 0600 temp file in dir, because the
// client's importer takes a file path. cleanup removes it.
func stdinToTempFile(stdin io.Reader, dir string) (string, func(), error) {
	if stdin == nil {
		return "", nil, errors.New("no input: pass a file, or pipe the cURL text on stdin")
	}
	data, err := io.ReadAll(io.LimitReader(stdin, maxCurlBytes))
	if err != nil {
		return "", nil, fmt.Errorf("read stdin: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".curl-*.tmp") // created 0600
	if err != nil {
		return "", nil, fmt.Errorf("create a temporary file: %w", err)
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, fmt.Errorf("write the temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close the temporary file: %w", err)
	}
	return tmp.Name(), cleanup, nil
}
