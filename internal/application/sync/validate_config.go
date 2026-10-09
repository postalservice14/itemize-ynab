package sync

import (
	"strings"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

// ValidateConfig checks the config against already-fetched YNAB data: every
// category_overrides target must be an eligible, unambiguous category, every
// exclude_categories entry must name a category in the plan and every
// mapped account ID must exist (and not be deleted). All bad entries are
// reported in one error that matches config.ErrInvalid.
func ValidateConfig(cfg config.YNAB, categories []ynab.Category, accounts []ynab.Account) error {
	eligible := ynab.EligibleCategories(categories, cfg.ExcludeCategories...)
	inPlan := make(map[string]struct{}, len(categories))
	for _, c := range categories {
		if !c.Deleted {
			inPlan[strings.ToLower(strings.TrimSpace(c.Name))] = struct{}{}
		}
	}
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
		func(name string) bool {
			_, ok := inPlan[strings.ToLower(strings.TrimSpace(name))]
			return ok
		},
		func(id string) bool {
			_, ok := known[id]
			return ok
		},
	)
}
