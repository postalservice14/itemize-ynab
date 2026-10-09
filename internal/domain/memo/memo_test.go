package memo

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestMarker(t *testing.T) {
	assert.Equal(t, "[itemize:walmart:123:4500:1]", Marker("walmart:123:4500:1"))
}

func TestHasMarker(t *testing.T) {
	assert.True(t, HasMarker("[itemize:abc]"))
	assert.True(t, HasMarker("groceries [itemize:walmart:1:2:1]"))
	assert.False(t, HasMarker(""))
	assert.False(t, HasMarker("itemize:abc"))
	assert.False(t, HasMarker("[itemizer:abc]"))
}

func TestAppendMarker(t *testing.T) {
	m := Marker("k1")
	tests := []struct {
		name string
		memo string
		want string
	}{
		{"empty memo", "", m},
		{"existing memo", "weekly shop", "weekly shop " + m},
		{"already marked same", "weekly shop " + m, "weekly shop " + m},
		{"multibyte memo", "café ☕", "café ☕ " + m},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AppendMarker(tc.memo, "k1"))
		})
	}
}

func TestAppendMarker_fitsExactlyAtLimit(t *testing.T) {
	m := Marker("k1")
	base := strings.Repeat("a", MaxLen-utf8.RuneCountInString(m)-1)
	got := AppendMarker(base, "k1")
	assert.Equal(t, base+" "+m, got)
	assert.Equal(t, MaxLen, utf8.RuneCountInString(got))
}

func TestAppendMarker_truncatesExistingMemoNotMarker(t *testing.T) {
	m := Marker("k1")
	base := strings.Repeat("a", MaxLen-utf8.RuneCountInString(m)) // one rune too long
	got := AppendMarker(base, "k1")
	assert.True(t, strings.HasSuffix(got, "… "+m), got)
	assert.LessOrEqual(t, utf8.RuneCountInString(got), MaxLen)
	assert.True(t, HasMarker(got))
}

func TestAppendMarker_multibyteTruncationIsRuneSafe(t *testing.T) {
	m := Marker("k1")
	base := strings.Repeat("日本語", 100)
	got := AppendMarker(base, "k1")
	assert.True(t, utf8.ValidString(got))
	assert.True(t, strings.HasSuffix(got, "… "+m))
	assert.Equal(t, MaxLen, utf8.RuneCountInString(got))
}

func TestAppendMarker_differentMarkerPresentStillAppends(t *testing.T) {
	got := AppendMarker(Marker("other"), "k1")
	assert.Equal(t, Marker("other")+" "+Marker("k1"), got)
}

func TestAppendMarker_markerAloneTooLong(t *testing.T) {
	key := strings.Repeat("k", MaxLen)
	assert.Equal(t, Marker(key), AppendMarker("memo", key))
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", Truncate("abc", 5))
	assert.Equal(t, "ab", Truncate("abc", 2))
	assert.Equal(t, "", Truncate("abc", 0))
	assert.Equal(t, "日本", Truncate("日本語", 2))
	assert.True(t, utf8.ValidString(Truncate("héllo wörld", 2)))
}
