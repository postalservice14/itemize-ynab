// Package splitter turns a Walmart charge and its categorized items into YNAB
// split amounts.
//
// The caller passes CHARGE amounts (what the card was actually billed), not
// the order total. They already include tax, fees and tip, so spreading them
// in proportion to each category's item subtotal IS the proportional
// distribution of tax/fees/tip: a category holding 60% of the item subtotal
// absorbs 60% of the charges. BuildSplits does this for one charge;
// BuildOrderSplits does it for all of an order's charges at once and then
// fills each charge from the category totals.
//
// Allocation happens in integer cents (largest-remainder, via the allocator
// package) and only then converts to YNAB milliunits (1 cent = 10 milliunits).
//
// If every item subtotal is zero there is nothing to weigh categories by, so
// the builders return ErrNoPositiveSubtotal rather than quietly spreading
// evenly or choosing a default category: an item-level outcome must never be
// invented without saying so.
package splitter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/postalservice14/itemize-ynab/internal/domain/allocator"
	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
)

const milliPerCent = 10

// Errors returned by BuildSplits. Use errors.Is to match. Unmapped categories
// are reported as *UnmappedCategoryError instead.
var (
	ErrNoItems            = errors.New("splitter: no items to split")
	ErrInvalidCharge      = errors.New("splitter: charge must be a positive amount that fits in milliunits")
	ErrInvalidSubtotal    = errors.New("splitter: category subtotal is negative or overflows")
	ErrNoPositiveSubtotal = errors.New("splitter: no item has a positive subtotal")
)

// Item is one categorized Walmart line item.
type Item struct {
	Name          string
	Category      string
	SubtotalCents int64
}

// CategoryResolver maps a category name to a YNAB category ID.
type CategoryResolver func(name string) (id string, ok bool)

// Split is one YNAB subtransaction. AmountMilli is negative (an outflow).
type Split struct {
	CategoryID  string
	AmountMilli int64
	Memo        string
}

// UnmappedCategoryError lists every category name the resolver could not map.
type UnmappedCategoryError struct {
	Names []string
}

func (e *UnmappedCategoryError) Error() string {
	return fmt.Sprintf("splitter: unmapped categories: %s", strings.Join(e.Names, ", "))
}

// group accumulates the items that end up in one YNAB category.
type group struct {
	id     string
	weight int64
	names  []string
}

// BuildSplits splits chargeCents across the items' categories. The returned
// milliunit amounts sum to exactly -chargeCents*10. Categories (by name) that
// resolve to the same YNAB ID are merged, and the memo lists the items of all
// merged categories. Splits are sorted largest to smallest by absolute amount,
// ties by CategoryID. A split whose allocated amount rounds to zero is omitted
// (YNAB gains nothing from a zero-amount subtransaction).
func BuildSplits(chargeCents int64, items []Item, resolve CategoryResolver) ([]Split, error) {
	if len(items) == 0 {
		return nil, ErrNoItems
	}
	if chargeCents <= 0 || chargeCents > (1<<63-1)/milliPerCent {
		return nil, fmt.Errorf("%w: %d cents", ErrInvalidCharge, chargeCents)
	}
	subtotals, order, err := categorySubtotals(items)
	if err != nil {
		return nil, err
	}
	groups, err := resolveGroups(items, subtotals, order, resolve)
	if err != nil {
		return nil, err
	}
	return allocate(chargeCents, groups)
}

// categorySubtotals sums subtotals per category name, returning the names in
// first-seen order. A negative or overflowing category sum is an error.
func categorySubtotals(items []Item) (map[string]int64, []string, error) {
	subtotals := make(map[string]int64)
	var order []string
	for _, it := range items {
		cur, seen := subtotals[it.Category]
		if !seen {
			order = append(order, it.Category)
		}
		if (it.SubtotalCents > 0 && cur > (1<<63-1)-it.SubtotalCents) ||
			(it.SubtotalCents < 0 && cur < -(1<<63-1)-it.SubtotalCents) {
			return nil, nil, fmt.Errorf("%w: category %q", ErrInvalidSubtotal, it.Category)
		}
		subtotals[it.Category] = cur + it.SubtotalCents
	}
	for _, name := range order {
		if subtotals[name] < 0 {
			return nil, nil, fmt.Errorf("%w: category %q totals %d", ErrInvalidSubtotal, name, subtotals[name])
		}
	}
	return subtotals, order, nil
}

// resolveGroups maps each category name to a YNAB ID and merges by ID. Every
// unmapped name is collected into a single error.
func resolveGroups(items []Item, subtotals map[string]int64, order []string, resolve CategoryResolver) ([]*group, error) {
	idByName := make(map[string]string, len(order))
	var unmapped []string
	for _, name := range order {
		id, ok := resolve(name)
		if !ok {
			unmapped = append(unmapped, name)
			continue
		}
		idByName[name] = id
	}
	if len(unmapped) > 0 {
		return nil, &UnmappedCategoryError{Names: unmapped}
	}
	byID := make(map[string]*group)
	var groups []*group
	for _, name := range order {
		id := idByName[name]
		g, ok := byID[id]
		if !ok {
			g = &group{id: id}
			byID[id] = g
			groups = append(groups, g)
		}
		g.weight += subtotals[name]
	}
	for _, it := range items {
		if it.Name == "" {
			continue
		}
		g := byID[idByName[it.Category]]
		g.names = append(g.names, it.Name)
	}
	return groups, nil
}

// allocate runs the cent allocation, converts to milliunits and sorts.
func allocate(chargeCents int64, groups []*group) ([]Split, error) {
	weights := make([]int64, len(groups))
	var positive bool
	for i, g := range groups {
		weights[i] = g.weight
		positive = positive || g.weight > 0
	}
	if !positive {
		return nil, ErrNoPositiveSubtotal
	}
	cents, err := allocator.Allocate(chargeCents, weights)
	if err != nil {
		return nil, fmt.Errorf("splitter: allocate: %w", err)
	}
	splits := make([]Split, 0, len(groups))
	for i, g := range groups {
		if cents[i] == 0 {
			continue
		}
		splits = append(splits, Split{
			CategoryID:  g.id,
			AmountMilli: -cents[i] * milliPerCent,
			Memo:        memo.Truncate(strings.Join(g.names, ", "), memo.MaxLen),
		})
	}
	sortSplits(splits)
	return splits, nil
}

// SingleCategory reports whether all splits fall in one YNAB category, so the
// writer can use the plain categorize path instead of a split.
func SingleCategory(splits []Split) (id string, ok bool) {
	if len(splits) != 1 {
		return "", false
	}
	return splits[0].CategoryID, true
}
