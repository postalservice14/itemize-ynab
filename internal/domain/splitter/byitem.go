package splitter

import (
	"fmt"

	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
)

// ByItem reports whether an order's items should be split one item per split
// rather than by category: there are at most maxItems of them (0 never) and
// none is negative, since a coupon cannot be a split of its own.
func ByItem(items []Item, maxItems int) bool {
	if maxItems <= 0 || len(items) > maxItems {
		return false
	}
	for _, it := range items {
		if it.SubtotalCents < 0 {
			return false
		}
	}
	return true
}

// itemGroups makes one group per item, in input order, each in its category's
// YNAB ID. Items sharing a category are not merged.
func itemGroups(items []Item, order []string, resolve CategoryResolver) ([]*group, error) {
	idByName, err := resolveNames(order, resolve)
	if err != nil {
		return nil, err
	}
	groups := make([]*group, len(items))
	for i, it := range items {
		if it.SubtotalCents < 0 {
			return nil, fmt.Errorf("%w: item %q is %d", ErrInvalidSubtotal, it.Name, it.SubtotalCents)
		}
		groups[i] = &group{id: idByName[it.Category], weight: it.SubtotalCents, items: []Item{it}, item: i + 1}
	}
	return groups, nil
}

// splitMemo is a group's memo: the item's name for a by-item group, else the
// category's item list with shares.
func splitMemo(g *group, cents int64) string {
	if g.item > 0 {
		return memo.Truncate(g.items[0].Name, memo.MaxLen)
	}
	return groupMemo(g, cents)
}
