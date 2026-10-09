package splitter

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mapResolver(m map[string]string) CategoryResolver {
	return func(name string) (string, bool) {
		id, ok := m[name]
		return id, ok
	}
}

func sumMilli(splits []Split) int64 {
	var s int64
	for _, sp := range splits {
		s += sp.AmountMilli
	}
	return s
}

func TestBuildSplits_proportionalTaxFeeTip(t *testing.T) {
	// Items total $100.00 but the charge is $108.50 (tax/fees/tip included).
	// Spreading the charge by item subtotal IS the proportional distribution:
	// $60 gets 60% of $108.50 = $65.10, $40 gets $43.40.
	items := []Item{
		{Name: "Paper towels", Category: "Household", SubtotalCents: 6000},
		{Name: "Apples", Category: "Groceries", SubtotalCents: 4000},
	}
	res := mapResolver(map[string]string{"Household": "id-house", "Groceries": "id-groc"})
	got, err := BuildSplits(10850, items, res)
	require.NoError(t, err)
	assert.Equal(t, []Split{
		{CategoryID: "id-house", AmountMilli: -65100, Memo: "Paper towels"},
		{CategoryID: "id-groc", AmountMilli: -43400, Memo: "Apples"},
	}, got)
	assert.Equal(t, int64(-108500), sumMilli(got))
}

func TestBuildSplits_mergesCategoriesResolvingToSameID(t *testing.T) {
	items := []Item{
		{Name: "Milk", Category: "Dairy", SubtotalCents: 300},
		{Name: "Bread", Category: "Bakery", SubtotalCents: 200},
		{Name: "Soap", Category: "Household", SubtotalCents: 400},
		{Name: "Eggs", Category: "Dairy", SubtotalCents: 100},
	}
	res := mapResolver(map[string]string{"Dairy": "food", "Bakery": "food", "Household": "home"})
	got, err := BuildSplits(1000, items, res)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "food", got[0].CategoryID)
	assert.Equal(t, int64(-6000), got[0].AmountMilli)
	assert.Equal(t, "Milk, Bread, Eggs", got[0].Memo)
	assert.Equal(t, "home", got[1].CategoryID)
	assert.Equal(t, int64(-4000), got[1].AmountMilli)
	assert.Equal(t, int64(-10000), sumMilli(got))
}

func TestBuildSplits_exactSumWithAwkwardRounding(t *testing.T) {
	items := []Item{
		{Name: "a", Category: "A", SubtotalCents: 1},
		{Name: "b", Category: "B", SubtotalCents: 1},
		{Name: "c", Category: "C", SubtotalCents: 1},
	}
	res := mapResolver(map[string]string{"A": "ia", "B": "ib", "C": "ic"})
	got, err := BuildSplits(100, items, res)
	require.NoError(t, err)
	assert.Equal(t, int64(-1000), sumMilli(got))
	// 34/33/33 cents; the tie goes to the lowest index (A), then sorted.
	assert.Equal(t, int64(-340), got[0].AmountMilli)
	assert.Equal(t, "ia", got[0].CategoryID)
	assert.Equal(t, "ib", got[1].CategoryID)
	assert.Equal(t, "ic", got[2].CategoryID)
}

func TestBuildSplits_sortTiesByCategoryID(t *testing.T) {
	items := []Item{
		{Name: "z", Category: "Z", SubtotalCents: 100},
		{Name: "a", Category: "A", SubtotalCents: 100},
	}
	res := mapResolver(map[string]string{"Z": "zzz", "A": "aaa"})
	got, err := BuildSplits(200, items, res)
	require.NoError(t, err)
	assert.Equal(t, "aaa", got[0].CategoryID)
	assert.Equal(t, "zzz", got[1].CategoryID)
}

func TestBuildSplits_unmappedCollectsAllNames(t *testing.T) {
	items := []Item{
		{Name: "x", Category: "Mystery", SubtotalCents: 100},
		{Name: "y", Category: "Known", SubtotalCents: 100},
		{Name: "z", Category: "Other", SubtotalCents: 100},
		{Name: "w", Category: "Mystery", SubtotalCents: 100},
	}
	res := mapResolver(map[string]string{"Known": "k"})
	got, err := BuildSplits(400, items, res)
	assert.Nil(t, got)
	var ue *UnmappedCategoryError
	require.True(t, errors.As(err, &ue))
	assert.Equal(t, []string{"Mystery", "Other"}, ue.Names)
	assert.Contains(t, err.Error(), "Mystery")
	assert.Contains(t, err.Error(), "Other")
}

func TestBuildSplits_errors(t *testing.T) {
	res := mapResolver(map[string]string{"A": "a"})
	ok := []Item{{Name: "n", Category: "A", SubtotalCents: 100}}
	tests := []struct {
		name   string
		charge int64
		items  []Item
		target error
	}{
		{"no items", 100, nil, ErrNoItems},
		{"zero charge", 0, ok, ErrInvalidCharge},
		{"negative charge", -5, ok, ErrInvalidCharge},
		{"charge too large for milliunits", 1 << 62, ok, ErrInvalidCharge},
		{"all zero subtotals", 100, []Item{{Name: "n", Category: "A"}}, ErrNoPositiveSubtotal},
		{"negative subtotal", 100, []Item{
			{Name: "n", Category: "A", SubtotalCents: 500},
			{Name: "d", Category: "A", SubtotalCents: -600},
		}, ErrInvalidSubtotal},
		{"subtotal overflow", 100, []Item{
			{Name: "n", Category: "A", SubtotalCents: 1<<63 - 1},
			{Name: "d", Category: "A", SubtotalCents: 1},
		}, ErrInvalidSubtotal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildSplits(tc.charge, tc.items, res)
			assert.Nil(t, got)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.target), "got %v", err)
		})
	}
}

func TestBuildSplits_negativeItemOffsetWithinCategoryIsAllowed(t *testing.T) {
	items := []Item{
		{Name: "Shirt", Category: "A", SubtotalCents: 1000},
		{Name: "Coupon", Category: "A", SubtotalCents: -200},
		{Name: "Hat", Category: "B", SubtotalCents: 800},
	}
	res := mapResolver(map[string]string{"A": "a", "B": "b"})
	got, err := BuildSplits(1600, items, res)
	require.NoError(t, err)
	assert.Equal(t, int64(-16000), sumMilli(got))
	assert.Equal(t, int64(-8000), got[0].AmountMilli)
}

func TestBuildSplits_zeroSubtotalCategoryIsDropped(t *testing.T) {
	items := []Item{
		{Name: "Bag", Category: "Free", SubtotalCents: 0},
		{Name: "Milk", Category: "Dairy", SubtotalCents: 300},
	}
	res := mapResolver(map[string]string{"Free": "free", "Dairy": "dairy"})
	got, err := BuildSplits(300, items, res)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "dairy", got[0].CategoryID)
	assert.Equal(t, int64(-3000), got[0].AmountMilli)
}

func TestBuildSplits_zeroSubtotalCategoryStillMustBeMapped(t *testing.T) {
	items := []Item{
		{Name: "Bag", Category: "Free", SubtotalCents: 0},
		{Name: "Milk", Category: "Dairy", SubtotalCents: 300},
	}
	_, err := BuildSplits(300, items, mapResolver(map[string]string{"Dairy": "d"}))
	var ue *UnmappedCategoryError
	require.True(t, errors.As(err, &ue))
	assert.Equal(t, []string{"Free"}, ue.Names)
}

func TestBuildSplits_roundsAwayTinyCategory(t *testing.T) {
	items := []Item{
		{Name: "tiny", Category: "T", SubtotalCents: 1},
		{Name: "big", Category: "B", SubtotalCents: 1000},
	}
	res := mapResolver(map[string]string{"T": "t", "B": "b"})
	got, err := BuildSplits(1, items, res)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "b", got[0].CategoryID)
	assert.Equal(t, int64(-10), got[0].AmountMilli)
}

func TestBuildSplits_memoTruncatedRuneSafe(t *testing.T) {
	long := strings.Repeat("日本語の商品名", 40) // 280 runes, 3 bytes each
	items := []Item{{Name: long, Category: "A", SubtotalCents: 100}}
	got, err := BuildSplits(100, items, mapResolver(map[string]string{"A": "a"}))
	require.NoError(t, err)
	assert.True(t, utf8.ValidString(got[0].Memo))
	assert.Equal(t, 200, utf8.RuneCountInString(got[0].Memo))
	assert.True(t, strings.HasPrefix(long, got[0].Memo), "hard truncation, no ellipsis")
}

func TestBuildSplits_memoJoinsNames(t *testing.T) {
	items := []Item{
		{Name: "A1", Category: "A", SubtotalCents: 100},
		{Name: "", Category: "A", SubtotalCents: 100},
		{Name: "A2", Category: "A", SubtotalCents: 100},
	}
	got, err := BuildSplits(300, items, mapResolver(map[string]string{"A": "a"}))
	require.NoError(t, err)
	assert.Equal(t, "A1, A2", got[0].Memo)
}

func TestSingleCategory(t *testing.T) {
	id, ok := SingleCategory([]Split{{CategoryID: "x"}})
	assert.True(t, ok)
	assert.Equal(t, "x", id)

	_, ok = SingleCategory([]Split{{CategoryID: "x"}, {CategoryID: "y"}})
	assert.False(t, ok)
	_, ok = SingleCategory(nil)
	assert.False(t, ok)
}
