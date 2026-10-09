package splitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The live order that showed the problem: one delivery charged in six pieces
// (the driver tip, charged separately, is not part of it).
func liveOrderItems() []Item {
	return []Item{
		{Name: "Blackberries", Category: "Groceries", SubtotalCents: 554},
		{Name: "Camp Chair", Category: "Camping", SubtotalCents: 2991},
		{Name: "Cat Food", Category: "Animals", SubtotalCents: 1868},
		{Name: "Dish Soap", Category: "Maintenance", SubtotalCents: 777},
		{Name: "Groceries rest", Category: "Groceries", SubtotalCents: 4260},
	}
}

var liveResolver = mapResolver(map[string]string{
	"Groceries": "groc", "Camping": "camp", "Animals": "pets", "Maintenance": "maint",
})

func TestBuildOrderSplits_fillsLargestChargesFirst(t *testing.T) {
	got, err := BuildOrderSplits([]int64{570, 579, 839, 372, 9021, 482}, liveOrderItems(), liveResolver)

	require.NoError(t, err)
	require.Len(t, got, 6, "one split list per charge, in input order")
	// Category totals over $118.63: groceries 5465, camping 3395, animals 2121, maintenance 882.
	assert.Equal(t, []Split{
		{CategoryID: "groc", AmountMilli: -54650, Memo: "Blackberries, Groceries rest"},
		{CategoryID: "camp", AmountMilli: -33950, Memo: "Camp Chair"},
		{CategoryID: "pets", AmountMilli: -1610, Memo: "Cat Food"},
	}, got[4], "$90.21 takes the largest categories")
	assert.Equal(t, []Split{{CategoryID: "pets", AmountMilli: -8390, Memo: "Cat Food"}}, got[2], "$8.39")
	assert.Equal(t, []Split{{CategoryID: "pets", AmountMilli: -5790, Memo: "Cat Food"}}, got[1], "$5.79")
	assert.Equal(t, []Split{
		{CategoryID: "pets", AmountMilli: -5420, Memo: "Cat Food"},
		{CategoryID: "maint", AmountMilli: -280, Memo: "Dish Soap"},
	}, got[0], "$5.70")
	assert.Equal(t, []Split{{CategoryID: "maint", AmountMilli: -4820, Memo: "Dish Soap"}}, got[5], "$4.82")
	assert.Equal(t, []Split{{CategoryID: "maint", AmountMilli: -3720, Memo: "Dish Soap"}}, got[3], "$3.72")

	totals := map[string]int64{}
	for i, splits := range got {
		assert.Equal(t, -[]int64{570, 579, 839, 372, 9021, 482}[i]*10, sumMilli(splits), "charge %d sums exactly", i)
		for _, s := range splits {
			totals[s.CategoryID] += s.AmountMilli
		}
	}
	assert.Equal(t, map[string]int64{"groc": -54650, "camp": -33950, "pets": -21210, "maint": -8820}, totals,
		"each category gets exactly its share of the order")
}

func TestBuildOrderSplits_oneChargeMatchesBuildSplits(t *testing.T) {
	items := []Item{
		{Name: "Paper towels", Category: "Household", SubtotalCents: 6000},
		{Name: "Apples", Category: "Groceries", SubtotalCents: 4000},
	}
	res := mapResolver(map[string]string{"Household": "id-house", "Groceries": "id-groc"})

	got, err := BuildOrderSplits([]int64{10850}, items, res)
	require.NoError(t, err)
	want, err := BuildSplits(10850, items, res)
	require.NoError(t, err)

	assert.Equal(t, [][]Split{want}, got)
}

func TestBuildOrderSplits_equalChargesKeepInputOrderForTies(t *testing.T) {
	items := []Item{{Name: "A", Category: "Groceries", SubtotalCents: 100}, {Name: "B", Category: "Camping", SubtotalCents: 100}}

	got, err := BuildOrderSplits([]int64{500, 500}, items, liveResolver)

	require.NoError(t, err)
	assert.Equal(t, [][]Split{
		{{CategoryID: "groc", AmountMilli: -5000, Memo: "A"}},
		{{CategoryID: "camp", AmountMilli: -5000, Memo: "B"}},
	}, got)
}

func TestBuildOrderSplits_errors(t *testing.T) {
	items := liveOrderItems()
	tests := []struct {
		name    string
		charges []int64
		items   []Item
		res     CategoryResolver
		want    error
	}{
		{"no charges", nil, items, liveResolver, ErrInvalidCharge},
		{"zero charge", []int64{100, 0}, items, liveResolver, ErrInvalidCharge},
		{"no items", []int64{100}, nil, liveResolver, ErrNoItems},
		{"no positive subtotal", []int64{100}, []Item{{Name: "x", Category: "Groceries"}}, liveResolver, ErrNoPositiveSubtotal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildOrderSplits(tc.charges, tc.items, tc.res)
			assert.ErrorIs(t, err, tc.want)
		})
	}
	_, err := BuildOrderSplits([]int64{100}, items, mapResolver(nil))
	var unmapped *UnmappedCategoryError
	assert.ErrorAs(t, err, &unmapped)
}

func TestMergeSplits_sumsByCategory(t *testing.T) {
	got := MergeSplits(
		[]Split{{CategoryID: "groc", AmountMilli: -1000, Memo: "Milk"}, {CategoryID: "camp", AmountMilli: -500, Memo: "Chair"}},
		[]Split{{CategoryID: "camp", AmountMilli: -2000, Memo: "Chair"}},
	)

	assert.Equal(t, []Split{
		{CategoryID: "camp", AmountMilli: -2500, Memo: "Chair"},
		{CategoryID: "groc", AmountMilli: -1000, Memo: "Milk"},
	}, got)
}
