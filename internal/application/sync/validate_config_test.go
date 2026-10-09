package sync

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

func TestValidateConfig_checksOverridesAndAccountsAgainstFetchedLists(t *testing.T) {
	cfg := config.YNAB{
		Accounts: map[string]string{"0001": "a-ok", "0002": "a-missing"},
		CategoryOverrides: map[string]string{
			"Pet Supplies": "Pets",       // eligible: fine
			"Old":          "Old Cat",    // hidden: not eligible
			"Gifts":        "Gifts",      // ambiguous
			"Cards":        "Chase Visa", // credit card payment group
			"Nope":         "Missing",
		},
	}
	cats := []ynab.Category{
		{ID: "1", Name: "Pets", GroupName: "Needs"},
		{ID: "2", Name: "Old Cat", GroupName: "Needs", Hidden: true},
		{ID: "3", Name: "Gifts", GroupName: "A"},
		{ID: "4", Name: "Gifts", GroupName: "B"},
		{ID: "5", Name: "Chase Visa", GroupName: "Credit Card Payments"},
	}
	accts := []ynab.Account{{ID: "a-ok"}, {ID: "a-closed-deleted", Deleted: true}}

	err := ValidateConfig(cfg, cats, accts)

	require.ErrorIs(t, err, config.ErrInvalid)
	msg := err.Error()
	for _, want := range []string{"Old Cat", "Gifts", "ambiguous", "Chase Visa", "Missing", "a-missing"} {
		assert.Contains(t, msg, want)
	}
	assert.NotContains(t, msg, `-> "Pets"`)
	assert.NotContains(t, msg, `"a-ok"`)
}

func TestValidateConfig_okWhenEverythingExists(t *testing.T) {
	cfg := config.YNAB{
		Accounts:          map[string]string{"0001": "a1"},
		CategoryOverrides: map[string]string{"Pet Supplies": "pets"},
	}

	err := ValidateConfig(cfg,
		[]ynab.Category{{ID: "1", Name: "Pets", GroupName: "Needs"}},
		[]ynab.Account{{ID: "a1"}})

	assert.NoError(t, err)
}
