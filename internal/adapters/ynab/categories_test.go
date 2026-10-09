package ynab_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
)

func TestEligibleCategories(t *testing.T) {
	cat := func(id, name, group string) ynab.Category {
		return ynab.Category{ID: id, Name: name, GroupName: group}
	}
	hidden := cat("h", "Hidden One", "Needs")
	hidden.Hidden = true
	deleted := cat("d", "Deleted One", "Needs")
	deleted.Deleted = true

	tests := []struct {
		name string
		in   []ynab.Category
		want []string
	}{
		{"plain categories kept in order", []ynab.Category{cat("1", "Groceries", "Needs"), cat("2", "Fun", "Wants")}, []string{"1", "2"}},
		{"hidden excluded", []ynab.Category{hidden, cat("1", "Groceries", "Needs")}, []string{"1"}},
		{"deleted excluded", []ynab.Category{deleted, cat("1", "Groceries", "Needs")}, []string{"1"}},
		{"internal master group excluded", []ynab.Category{
			cat("i1", "Inflow: Ready to Assign", "Internal Master Category"),
			cat("i2", "Uncategorized", "Internal Master Category"),
			cat("1", "Groceries", "Needs")}, []string{"1"}},
		{"inflow category excluded wherever it lives", []ynab.Category{
			cat("i1", "Inflow: Ready to Assign", "Somewhere"), cat("1", "Groceries", "Needs")}, []string{"1"}},
		{"credit card payments group excluded", []ynab.Category{
			cat("c1", "Chase Visa", "Credit Card Payments"), cat("1", "Groceries", "Needs")}, []string{"1"}},
		{"group match is case-insensitive and trimmed", []ynab.Category{
			cat("c1", "Chase Visa", "  credit card payments "), cat("1", "Groceries", "Needs")}, []string{"1"}},
		{"empty input", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, c := range ynab.EligibleCategories(tc.in) {
				got = append(got, c.ID)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEligibleCategories_excludedNames(t *testing.T) {
	cats := []ynab.Category{
		{ID: "1", Name: "Groceries", GroupName: "Food"},
		{ID: "2", Name: "Mortgage", GroupName: "Housing"},
		{ID: "3", Name: "Misc", GroupName: "Housing"},
		{ID: "4", Name: "Mortgage", GroupName: "Old"},
	}
	ids := func(in []ynab.Category) []string {
		var out []string
		for _, c := range in {
			out = append(out, c.ID)
		}
		return out
	}

	assert.Equal(t, []string{"1", "3"}, ids(ynab.EligibleCategories(cats, "Mortgage")),
		"every category with an excluded name is dropped, whatever its group")
	assert.Equal(t, []string{"1", "3"}, ids(ynab.EligibleCategories(cats, "  mortgage ")),
		"names match ignoring case and surrounding space")
	assert.Equal(t, []string{"3"}, ids(ynab.EligibleCategories(cats, "Groceries", "Mortgage")))
	assert.Equal(t, []string{"1", "2", "3", "4"}, ids(ynab.EligibleCategories(cats)), "no exclusions keeps everything")
}

func TestFindCategoryByName(t *testing.T) {
	cats := []ynab.Category{
		{ID: "1", Name: "Pets", GroupName: "Needs"},
		{ID: "2", Name: "Groceries", GroupName: "Needs"},
		{ID: "3", Name: "Gifts", GroupName: "Wants"},
		{ID: "4", Name: "gifts", GroupName: "Giving"},
	}

	t.Run("case-insensitive unique match", func(t *testing.T) {
		got, err := ynab.FindCategoryByName(cats, "  PETS ")
		require.NoError(t, err)
		assert.Equal(t, "1", got.ID)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := ynab.FindCategoryByName(cats, "Boats")
		require.ErrorIs(t, err, ynab.ErrCategoryNotFound)
		assert.Contains(t, err.Error(), "Boats")
	})

	t.Run("ambiguous lists the groups", func(t *testing.T) {
		_, err := ynab.FindCategoryByName(cats, "Gifts")
		require.ErrorIs(t, err, ynab.ErrCategoryAmbiguous)
		var amb *ynab.AmbiguousCategoryError
		require.True(t, errors.As(err, &amb))
		assert.Equal(t, []string{"Wants", "Giving"}, amb.Groups)
		assert.Contains(t, err.Error(), "Wants")
		assert.Contains(t, err.Error(), "Giving")
	})
}
