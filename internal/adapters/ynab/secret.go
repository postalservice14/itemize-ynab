package ynab

import (
	"fmt"
	"log/slog"
	"strings"
)

const redacted = "REDACTED"

// secret holds the personal access token. Every way of printing it, whether
// fmt verbs, slog or JSON, yields a redaction marker.
type secret string

func (secret) String() string               { return redacted }
func (secret) GoString() string             { return redacted }
func (secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (s secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }
func (s secret) scrub(text string) string {
	if s == "" {
		return text
	}
	return strings.ReplaceAll(text, string(s), redacted)
}
