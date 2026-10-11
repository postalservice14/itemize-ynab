package splitter

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/postalservice14/itemize-ynab/internal/domain/allocator"
	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
)

// more ends a memo whose item list was cut short.
const more = ", …"

// groupMemo lists a group's named items, each with its share of the group's
// cents in parentheses: "Milk ($3.77), Bread ($2.71)". The shares split cents
// by item subtotal (largest remainder), so they include the item's part of
// tax, fees and tip and add up to the group's total; nameless items keep
// their share but are not listed. A group holding a negative item (a coupon,
// say) has no meaningful per-item share and lists names only.
func groupMemo(g *group, cents int64) string {
	weights := make([]int64, len(g.items))
	for i, it := range g.items {
		weights[i] = it.SubtotalCents
	}
	shares, err := allocator.Allocate(cents, weights)
	var entries []string
	for i, it := range g.items {
		if it.Name == "" {
			continue
		}
		if err != nil {
			entries = append(entries, it.Name)
			continue
		}
		entries = append(entries, fmt.Sprintf("%s (%s)", it.Name, formatCents(shares[i])))
	}
	return joinEntries(entries)
}

// joinEntries joins entries with ", " within memo.MaxLen. When they do not
// all fit, it keeps the whole entries that do and ends with "…", so no amount
// is ever cut in half. A first entry too long on its own is hard-truncated.
func joinEntries(entries []string) string {
	full := strings.Join(entries, ", ")
	if utf8.RuneCountInString(full) <= memo.MaxLen {
		return full
	}
	room := memo.MaxLen - utf8.RuneCountInString(more)
	out := ""
	for _, e := range entries {
		next := e
		if out != "" {
			next = out + ", " + e
		}
		if utf8.RuneCountInString(next) > room {
			break
		}
		out = next
	}
	if out == "" {
		return memo.Truncate(full, memo.MaxLen)
	}
	return out + more
}

// formatCents renders non-negative cents as dollars: 377 -> "$3.77".
func formatCents(cents int64) string {
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}
