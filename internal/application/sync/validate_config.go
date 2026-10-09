package sync

import (
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

// ValidateConfig checks the config against already-fetched YNAB data: every
// category_overrides target must be an eligible, unambiguous category and every
// mapped account ID must exist (and not be deleted). All bad entries are
// reported in one error that matches config.ErrInvalid.
func ValidateConfig(cfg config.YNAB, categories []ynab.Category, accounts []ynab.Account) error {
	eligible := ynab.EligibleCategories(categories)
	known := make(map[string]struct{}, len(accounts))
	for _, a := range accounts {
		if !a.Deleted {
			known[a.ID] = struct{}{}
		}
	}
	return cfg.CrossCheck(
		func(name string) error {
			_, err := ynab.FindCategoryByName(eligible, name)
			return err
		},
		func(id string) bool {
			_, ok := known[id]
			return ok
		},
	)
}
