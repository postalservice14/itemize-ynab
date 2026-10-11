package categorizer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

type replyItem struct {
	Index    *int    `json:"index"`
	Category *string `json:"category"`
	// Short is raw so a non-string value cannot reject the whole reply.
	Short json.RawMessage `json:"short"`
}

// pick is the validated model answer for one item.
type pick struct {
	category string
	short    string
}

type reply struct {
	Items []replyItem `json:"items"`
}

// parseReply leniently finds the first JSON object carrying an "items" array,
// skipping any prose or code fences around it.
func parseReply(text string) (reply, bool) {
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		var r reply
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		if err := dec.Decode(&r); err == nil && r.Items != nil {
			return r, true
		}
	}
	return reply{}, false
}

// interpret validates a model reply strictly against chunk and vocab. It
// returns the canonical model category and short name per chunk item, or the
// problems found. Only categories are strict; see shortName.
func interpret(text string, chunk []order.Item, v vocab) ([]pick, []string) {
	r, ok := parseReply(text)
	if !ok {
		return nil, []string{`the reply was not valid JSON of the form {"items":[{"index":0,"category":"..."}]}`}
	}
	out := make([]pick, len(chunk))
	seen := make(map[int]bool, len(chunk))
	var problems []string
	for _, it := range r.Items {
		problems = append(problems, checkItem(it, chunk, v, seen, out)...)
	}
	for i := range chunk {
		if !seen[i] {
			problems = append(problems, fmt.Sprintf("missing index %d (%s)", i, quote(chunk[i].Name)))
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		return nil, problems
	}
	return out, nil
}

func checkItem(it replyItem, chunk []order.Item, v vocab, seen map[int]bool, out []pick) []string {
	if it.Index == nil {
		return []string{"an entry has no index"}
	}
	idx := *it.Index
	if idx < 0 || idx >= len(chunk) {
		return []string{fmt.Sprintf("index %d is out of range (valid: 0..%d)", idx, len(chunk)-1)}
	}
	name := quote(chunk[idx].Name)
	if seen[idx] {
		return []string{fmt.Sprintf("duplicate index %d (%s)", idx, name)}
	}
	seen[idx] = true
	if it.Category == nil || *it.Category == "" {
		return []string{fmt.Sprintf("index %d (%s) has no category", idx, name)}
	}
	canon, ok := v.canonical(*it.Category)
	if !ok {
		return []string{fmt.Sprintf("index %d (%s) has unknown category %s, which is not in the allowed list", idx, name, quote(*it.Category))}
	}
	out[idx] = pick{category: canon, short: shortName(it.Short)}
	return nil
}

// shortName returns the model's trimmed short name, or "" when it is missing,
// not a string, longer than maxShortLen runes, or holds a control character or
// an itemize marker (in any case; a marker in a memo makes the matcher treat
// the transaction as processed). Short names only decorate memos, so a bad one
// is dropped quietly instead of failing the categorization or costing a
// repair call.
func shortName(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxShortLen || strings.IndexFunc(s, unicode.IsControl) >= 0 ||
		memo.HasMarker(strings.ToLower(s)) {
		return ""
	}
	return s
}

// quote renders untrusted text as a short, escaped Go string.
func quote(s string) string {
	return fmt.Sprintf("%q", truncateRunes(s, 80))
}
