// Package version exposes the build version of the itemize-ynab binary.
package version

// Version is overridden at build time via -ldflags "-X .../internal/version.Version=...".
var Version = "dev"

// String returns the human-readable version line.
func String() string {
	return "itemize-ynab " + Version
}
