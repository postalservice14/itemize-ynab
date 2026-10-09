package version

import (
	"strings"
	"testing"
)

func TestString_defaultVersion_includesNameAndVersion(t *testing.T) {
	got := String()

	if !strings.HasPrefix(got, "itemize-ynab ") {
		t.Fatalf("String() = %q, want prefix %q", got, "itemize-ynab ")
	}
	if !strings.HasSuffix(got, Version) {
		t.Fatalf("String() = %q, want suffix %q", got, Version)
	}
}
