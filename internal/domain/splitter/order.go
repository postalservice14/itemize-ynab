package splitter

import (
	"fmt"
	"sort"
	"strings"

	"github.com/postalservice14/itemize-ynab/internal/domain/allocator"
	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
)

// BuildOrderSplits splits all of an order's charges at once and returns one
// split list per charge, in input order.
//
// The retailer does not say which items a charge paid for, so per charge the
// categories are a choice; per order they are not. Each category's total
// across the charges is its proportional share of their sum (largest
// remainder, as in BuildSplits), and the charges are then filled from those
// totals, largest charge first and largest category first, so each charge
// spans as few categories as possible. Ties keep input order. One charge gives
// exactly what BuildSplits gives.
func BuildOrderSplits(chargesCents []int64, items []Item, resolve CategoryResolver) ([][]Split, error) {
	total, err := chargeTotal(chargesCents)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNoItems
	}
	subtotals, order, err := categorySubtotals(items)
	if err != nil {
		return nil, err
	}
	groups, err := resolveGroups(items, subtotals, order, resolve)
	if err != nil {
		return nil, err
	}
	targets, err := categoryTargets(total, groups)
	if err != nil {
		return nil, err
	}
	return fill(chargesCents, groups, targets), nil
}

func chargeTotal(chargesCents []int64) (int64, error) {
	if len(chargesCents) == 0 {
		return 0, fmt.Errorf("%w: no charges", ErrInvalidCharge)
	}
	var total int64
	for _, c := range chargesCents {
		if c <= 0 || c > (1<<63-1)/milliPerCent-total {
			return 0, fmt.Errorf("%w: %d cents", ErrInvalidCharge, c)
		}
		total += c
	}
	return total, nil
}

// categoryTargets is each group's share of total, in cents.
func categoryTargets(total int64, groups []*group) ([]int64, error) {
	weights := make([]int64, len(groups))
	var positive bool
	for i, g := range groups {
		weights[i] = g.weight
		positive = positive || g.weight > 0
	}
	if !positive {
		return nil, ErrNoPositiveSubtotal
	}
	targets, err := allocator.Allocate(total, weights)
	if err != nil {
		return nil, fmt.Errorf("splitter: allocate: %w", err)
	}
	return targets, nil
}

// fill takes each charge, largest first, from the categories with the largest
// targets first.
func fill(chargesCents []int64, groups []*group, targets []int64) [][]Split {
	chargeOrder := descending(chargesCents)
	catOrder := descending(targets)
	remaining := append([]int64(nil), targets...)
	out := make([][]Split, len(chargesCents))
	next := 0
	for _, ci := range chargeOrder {
		need := chargesCents[ci]
		for need > 0 {
			gi := catOrder[next]
			take := min(need, remaining[gi])
			if take > 0 {
				out[ci] = append(out[ci], splitFor(groups[gi], take))
				remaining[gi] -= take
				need -= take
			}
			if remaining[gi] == 0 {
				next++
			}
		}
		sortSplits(out[ci])
	}
	return out
}

func splitFor(g *group, cents int64) Split {
	return Split{
		CategoryID:  g.id,
		AmountMilli: -cents * milliPerCent,
		Memo:        memo.Truncate(strings.Join(g.names, ", "), memo.MaxLen),
	}
}

// descending returns the indexes of values, largest value first, ties in
// index order.
func descending(values []int64) []int {
	idx := make([]int, len(values))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return values[idx[a]] > values[idx[b]] })
	return idx
}

// MergeSplits combines split lists into one, summing amounts per category and
// keeping each category's first memo. The result is sorted like BuildSplits.
func MergeSplits(lists ...[]Split) []Split {
	var out []Split
	pos := map[string]int{}
	for _, list := range lists {
		for _, s := range list {
			if i, ok := pos[s.CategoryID]; ok {
				out[i].AmountMilli += s.AmountMilli
				continue
			}
			pos[s.CategoryID] = len(out)
			out = append(out, s)
		}
	}
	sortSplits(out)
	return out
}

// sortSplits orders splits largest to smallest by absolute amount, ties by
// CategoryID.
func sortSplits(splits []Split) {
	sort.SliceStable(splits, func(a, b int) bool {
		if splits[a].AmountMilli != splits[b].AmountMilli {
			return splits[a].AmountMilli < splits[b].AmountMilli // more negative = larger
		}
		return splits[a].CategoryID < splits[b].CategoryID
	})
}
