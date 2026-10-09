package sync

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
)

func testCatalog() *catalog {
	return newCatalog([]ynab.Category{
		{ID: "c1", Name: "Groceries", GroupName: "Food"},
		{ID: "c2", Name: "Gifts", GroupName: "Family"},
		{ID: "c3", Name: "gifts", GroupName: "Holidays"},
		{ID: "c4", Name: "Old", GroupName: "Food", Hidden: true},
		{ID: "c5", Name: "Gone", GroupName: "Food", Deleted: true},
		{ID: "c6", Name: "Inflow: Ready to Assign", GroupName: "Internal Master Category"},
		{ID: "c7", Name: "Visa", GroupName: "Credit Card Payments"},
	})
}

func TestCatalog_allowedIsEligibleNamesDeduplicated(t *testing.T) {
	assert.Equal(t, []string{"Groceries", "Gifts"}, testCatalog().allowed())
}

func TestCatalog_resolve(t *testing.T) {
	c := testCatalog()
	cases := map[string]struct {
		name string
		id   string
		ok   bool
	}{
		"exact":            {"Groceries", "c1", true},
		"case and space":   {"  groceries ", "c1", true},
		"ambiguous":        {"Gifts", "", false},
		"hidden":           {"Old", "", false},
		"internal":         {"Inflow: Ready to Assign", "", false},
		"credit card":      {"Visa", "", false},
		"unknown":          {"Gadgets", "", false},
		"deleted excluded": {"Gone", "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			id, ok := c.resolve(tc.name)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.id, id)
		})
	}
	assert.Equal(t, "Groceries", c.name("c1"))
	assert.Equal(t, "", c.name("nope"))
}

func TestCatalog_explainUnmapped_namesCategoriesAndGroups(t *testing.T) {
	msg := testCatalog().explainUnmapped(&splitter.UnmappedCategoryError{Names: []string{"Gadgets", "Gifts"}})

	assert.Contains(t, msg, `"Gadgets"`)
	assert.Contains(t, msg, "not found")
	assert.Contains(t, msg, `"Gifts"`)
	assert.Contains(t, msg, "Family")
	assert.Contains(t, msg, "Holidays")
}

type scriptedCategorizer []categorizer.Assignment

func (s scriptedCategorizer) Categorize(context.Context, []order.Item, []string) ([]categorizer.Assignment, error) {
	return s, nil
}

func TestCategorizeItems(t *testing.T) {
	items := []order.Item{item("Kibble", 300), item("Milk", 200)}
	overrides := map[string]string{"pet supplies": "Pets"}

	t.Run("overrides apply to the model's category", func(t *testing.T) {
		got, err := categorizeItems(context.Background(), scriptedCategorizer{
			{ItemIndex: 1, Name: "Milk", Category: "Groceries", ModelCategory: "Groceries"},
			{ItemIndex: 0, Name: "Kibble", Category: "Pets", ModelCategory: "Pet Supplies"},
		}, items, nil, overrides)
		require.NoError(t, err)
		assert.Equal(t, []splitter.Item{
			{Name: "Kibble", Category: "Pets", SubtotalCents: 300},
			{Name: "Milk", Category: "Groceries", SubtotalCents: 200},
		}, got)
	})
	t.Run("category without model category", func(t *testing.T) {
		got, err := categorizeItems(context.Background(), scriptedCategorizer{
			{ItemIndex: 0, Category: "Pet Supplies"}, {ItemIndex: 1, Category: "Groceries"},
		}, items, nil, overrides)
		require.NoError(t, err)
		assert.Equal(t, "Pets", got[0].Category)
	})
	t.Run("missing item", func(t *testing.T) {
		_, err := categorizeItems(context.Background(), scriptedCategorizer{
			{ItemIndex: 0, Category: "Groceries"},
		}, items, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "item 1")
	})
	t.Run("index out of range", func(t *testing.T) {
		_, err := categorizeItems(context.Background(), scriptedCategorizer{
			{ItemIndex: 0, Category: "Groceries"}, {ItemIndex: 5, Category: "Groceries"},
		}, items, nil, nil)
		require.Error(t, err)
	})
	t.Run("empty category", func(t *testing.T) {
		_, err := categorizeItems(context.Background(), scriptedCategorizer{
			{ItemIndex: 0, Category: "Groceries"}, {ItemIndex: 1},
		}, items, nil, nil)
		require.Error(t, err)
	})
}

func TestOfferedCategories(t *testing.T) {
	allowed := []string{"Groceries", "Household"}
	cases := map[string]struct {
		overrides map[string]string
		want      []string
	}{
		"no overrides":         {nil, []string{"Groceries", "Household"}},
		"new keys sorted":      {map[string]string{"Toys": "Household", "Pet Supplies": "Household"}, []string{"Groceries", "Household", "Pet Supplies", "Toys"}},
		"key in other case":    {map[string]string{"groceries": "Household"}, []string{"Groceries", "Household"}},
		"keys equal fold once": {map[string]string{"Pets": "Household", "PETS": "Household"}, []string{"Groceries", "Household", "PETS"}},
		"empty key ignored":    {map[string]string{" ": "Household"}, []string{"Groceries", "Household"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, offeredCategories(allowed, tc.overrides))
		})
	}
	assert.Equal(t, []string{"Groceries", "Household"}, allowed, "input not modified")
}
