package splitter

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSplits_memoAmountsIncludeTaxAndSumToSplit(t *testing.T) {
	// $10.00 of items charged $10.83: each item carries its share of the tax.
	items := []Item{
		{Name: "Milk", Category: "Groceries", SubtotalCents: 348},
		{Name: "Bread", Category: "Groceries", SubtotalCents: 250},
		{Name: "Eggs", Category: "Groceries", SubtotalCents: 402},
	}
	got, err := BuildSplits(1083, items, mapResolver(map[string]string{"Groceries": "groc"}))
	require.NoError(t, err)
	require.Len(t, got, 1)
	// 1083 by 348/250/402: 376.88, 270.75, 435.37 -> 377, 271, 435 (sum 1083).
	assert.Equal(t, "Milk ($3.77), Bread ($2.71), Eggs ($4.35)", got[0].Memo)
}

func TestBuildSplits_memoWithNegativeItemListsNamesOnly(t *testing.T) {
	items := []Item{
		{Name: "Milk", Category: "Groceries", SubtotalCents: 500},
		{Name: "Coupon", Category: "Groceries", SubtotalCents: -100},
	}
	got, err := BuildSplits(400, items, mapResolver(map[string]string{"Groceries": "groc"}))
	require.NoError(t, err)
	assert.Equal(t, "Milk, Coupon", got[0].Memo)
}

func TestBuildSplits_memoTooLongKeepsWholeEntries(t *testing.T) {
	var items []Item
	for i := range 30 {
		items = append(items, Item{Name: fmt.Sprintf("Item number %02d", i), Category: "A", SubtotalCents: 100})
	}
	got, err := BuildSplits(3000, items, mapResolver(map[string]string{"A": "a"}))
	require.NoError(t, err)
	m := got[0].Memo
	assert.LessOrEqual(t, utf8.RuneCountInString(m), memo.MaxLen)
	assert.True(t, strings.HasPrefix(m, "Item number 00 ($1.00), Item number 01 ($1.00)"), m)
	assert.True(t, strings.HasSuffix(m, " ($1.00), …"), "ends after a whole entry: %s", m)
}

func TestFormatCents(t *testing.T) {
	assert.Equal(t, "$0.05", formatCents(5))
	assert.Equal(t, "$12.30", formatCents(1230))
	assert.Equal(t, "$1234.56", formatCents(123456))
}
