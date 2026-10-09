// Package memo holds the YNAB memo rules shared by the splitter and matcher:
// the itemize idempotency marker and rune-safe truncation. It exists so both
// packages can use the marker without importing each other.
package memo

import (
	"strings"
	"unicode/utf8"
)

// MaxLen is YNAB's memo limit, in characters (runes).
const MaxLen = 200

const (
	markerPrefix = "[itemize:"
	markerSuffix = "]"
	ellipsis     = "…"
)

// Marker returns the idempotency marker for a charge key: [itemize:<key>].
func Marker(key string) string {
	return markerPrefix + key + markerSuffix
}

// HasMarker reports whether memo carries any itemize marker.
func HasMarker(memo string) bool {
	return strings.Contains(memo, markerPrefix)
}

// Truncate returns s cut to at most limit runes. It never splits a multibyte
// rune and adds no ellipsis.
func Truncate(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// AppendMarker appends the marker for key to memo, separated by one space.
// An empty memo yields just the marker, and a memo already containing this
// exact marker is returned unchanged. If the result would exceed MaxLen runes,
// the EXISTING memo is truncated (ending in "…") so the marker always survives
// intact. If the marker alone cannot fit, only the marker is returned.
func AppendMarker(memo, key string) string {
	marker := Marker(key)
	if strings.Contains(memo, marker) {
		return memo
	}
	if memo == "" {
		return marker
	}
	markerLen := utf8.RuneCountInString(marker)
	if utf8.RuneCountInString(memo)+1+markerLen <= MaxLen {
		return memo + " " + marker
	}
	room := MaxLen - markerLen - 1 - utf8.RuneCountInString(ellipsis)
	if room <= 0 {
		return marker
	}
	return Truncate(memo, room) + ellipsis + " " + marker
}
