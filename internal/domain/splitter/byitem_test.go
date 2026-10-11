package splitter

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
)

var homeResolver = mapResolver(map[string]string{"Groceries": "groc", "Household": "home"})

func TestByItem(t *testing.T) {
	ten := make([]Item, 10)
	for i := range ten {
		ten[i] = Item{Name: "x", Category: "Groceries", SubtotalCents: 100}
	}
	eleven := append(append([]Item(nil), ten...), Item{Name: "y", Category: "Groceries", SubtotalCents: 100})
	coupon := []Item{{Name: "Milk", Category: "Groceries", SubtotalCents: 400}, {Name: "Coupon", Category: "Groceries", SubtotalCents: -100}}
	tests := []struct {
		name  string
		items []Item
		max   int
		want  bool
	}{
		{"at the limit", ten, 10, true},
		{"over the limit", eleven, 10, false},
		{"zero turns it off", ten, 0, false},
		{"a negative item falls back to categories", coupon, 10, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ByItem(tc.items, tc.max))
		})
	}
}

func TestBuildOrderSplits_byItemGivesOneSplitPerItem(t *testing.T) {
	items := []Item{
		{Name: "Milk", Category: "Groceries", SubtotalCents: 400},
		{Name: "Bread", Category: "Groceries", SubtotalCents: 300},
		{Name: "Eggs", Category: "Groceries", SubtotalCents: 250},
		{Name: "Apples", Category: "Groceries", SubtotalCents: 50},
		{Name: "Soap", Category: "Household", SubtotalCents: 600},
		{Name: "Towels", Category: "Household", SubtotalCents: 400},
	}

	got, err := BuildOrderSplits([]int64{2200}, items, homeResolver, true)

	require.NoError(t, err)
	assert.Equal(t, [][]Split{{
		{CategoryID: "home", AmountMilli: -6600, Memo: "Soap", item: 5},
		{CategoryID: "groc", AmountMilli: -4400, Memo: "Milk", item: 1},
		{CategoryID: "home", AmountMilli: -4400, Memo: "Towels", item: 6},
		{CategoryID: "groc", AmountMilli: -3300, Memo: "Bread", item: 2},
		{CategoryID: "groc", AmountMilli: -2750, Memo: "Eggs", item: 3},
		{CategoryID: "groc", AmountMilli: -550, Memo: "Apples", item: 4},
	}}, got, "each item carries its share of tax and fees, same-category items stay separate")
}

func TestBuildOrderSplits_byItemFillsChargesLargestFirst(t *testing.T) {
	items := []Item{
		{Name: "Chair", Category: "Household", SubtotalCents: 600},
		{Name: "Milk", Category: "Groceries", SubtotalCents: 400},
	}

	got, err := BuildOrderSplits([]int64{300, 700}, items, homeResolver, true)

	require.NoError(t, err)
	assert.Equal(t, [][]Split{
		{{CategoryID: "groc", AmountMilli: -3000, Memo: "Milk", item: 2}},
		{{CategoryID: "home", AmountMilli: -6000, Memo: "Chair", item: 1}, {CategoryID: "groc", AmountMilli: -1000, Memo: "Milk", item: 2}},
	}, got, "an item spread over two charges shows on both")
}

func TestBuildOrderSplits_byItemOmitsZeroItemsAndTruncatesLongNames(t *testing.T) {
	long := strings.Repeat("Very long item name ", 20)
	items := []Item{
		{Name: long, Category: "Groceries", SubtotalCents: 500},
		{Name: "Free sample", Category: "Groceries", SubtotalCents: 0},
	}

	got, err := BuildOrderSplits([]int64{500}, items, homeResolver, true)

	require.NoError(t, err)
	require.Len(t, got[0], 1)
	assert.LessOrEqual(t, utf8.RuneCountInString(got[0][0].Memo), memo.MaxLen)
	assert.True(t, strings.HasPrefix(long, strings.TrimSuffix(got[0][0].Memo, "…")))
}

func TestBuildOrderSplits_byItemErrors(t *testing.T) {
	_, err := BuildOrderSplits([]int64{100}, []Item{{Name: "x", Category: "Groceries"}}, homeResolver, true)
	require.ErrorIs(t, err, ErrNoPositiveSubtotal)

	_, err = BuildOrderSplits([]int64{100}, []Item{{Name: "x", Category: "Groceries", SubtotalCents: 500},
		{Name: "Coupon", Category: "Groceries", SubtotalCents: -100}}, homeResolver, true)
	require.ErrorIs(t, err, ErrInvalidSubtotal)

	_, err = BuildOrderSplits([]int64{100}, []Item{{Name: "a", Category: "Toys", SubtotalCents: 1},
		{Name: "b", Category: "Toys", SubtotalCents: 1}, {Name: "c", Category: "Tools", SubtotalCents: 1}}, homeResolver, true)
	var unmapped *UnmappedCategoryError
	require.ErrorAs(t, err, &unmapped)
	assert.Equal(t, []string{"Toys", "Tools"}, unmapped.Names)
}

func TestMergeSplits_keepsItemSplitsSeparate(t *testing.T) {
	got := MergeSplits(
		[]Split{{CategoryID: "groc", AmountMilli: -1000, Memo: "Milk", item: 1}, {CategoryID: "groc", AmountMilli: -500, Memo: "Bread", item: 2}},
		[]Split{{CategoryID: "groc", AmountMilli: -200, Memo: "Milk", item: 1}, {CategoryID: "groc", AmountMilli: -300, Memo: "Milk ($1.00), Bread ($2.00)"}},
	)

	assert.Equal(t, []Split{
		{CategoryID: "groc", AmountMilli: -1200, Memo: "Milk", item: 1},
		{CategoryID: "groc", AmountMilli: -500, Memo: "Bread", item: 2},
		{CategoryID: "groc", AmountMilli: -300, Memo: "Milk ($1.00), Bread ($2.00)"},
	}, got, "an item's pieces merge; a category split (a tip) stays its own line")
}
