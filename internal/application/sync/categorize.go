package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
)

// catalog is the run's YNAB category data, fetched once: the eligible
// categories (ynab.EligibleCategories), the names offered to the categorizer
// and the ID-to-name lookup for the report.
type catalog struct {
	eligible []ynab.Category
	names    []string
	byID     map[string]string
}

func newCatalog(all []ynab.Category, excluded []string) *catalog {
	c := &catalog{eligible: ynab.EligibleCategories(all, excluded...), byID: map[string]string{}}
	seen := map[string]bool{}
	for _, cat := range c.eligible {
		c.byID[cat.ID] = cat.Name
		key := strings.ToLower(strings.TrimSpace(cat.Name))
		if !seen[key] {
			seen[key] = true
			c.names = append(c.names, cat.Name)
		}
	}
	return c
}

// allowed returns the eligible category names, each once (case-insensitive).
func (c *catalog) allowed() []string { return c.names }

// resolve is the splitter.CategoryResolver: a case-insensitive lookup among
// the eligible categories. Unknown and ambiguous names are not resolved;
// explainUnmapped says which was which.
func (c *catalog) resolve(name string) (string, bool) {
	cat, err := ynab.FindCategoryByName(c.eligible, name)
	if err != nil {
		return "", false
	}
	return cat.ID, true
}

// name returns the category name of an eligible category ID, or "".
func (c *catalog) name(id string) string { return c.byID[id] }

// explainUnmapped names every unmapped category and why it did not resolve:
// not found among the eligible categories, or ambiguous across the named
// groups.
func (c *catalog) explainUnmapped(e *splitter.UnmappedCategoryError) string {
	parts := make([]string, 0, len(e.Names))
	for _, n := range e.Names {
		_, err := ynab.FindCategoryByName(c.eligible, n)
		var amb *ynab.AmbiguousCategoryError
		switch {
		case errors.As(err, &amb):
			parts = append(parts, fmt.Sprintf("%q is ambiguous (groups: %s)", n, strings.Join(amb.Groups, ", ")))
		default:
			parts = append(parts, fmt.Sprintf("%q not found among the plan's eligible categories", n))
		}
	}
	return "unmapped category: " + strings.Join(parts, "; ") + "; no default category is used"
}

// categorizeItems runs the categorizer once for an order's items and returns
// them with their final category names. Overrides are applied here to the
// model's own category (Assignment.ModelCategory), which makes the result the
// same whether or not the categorizer applied them already; an assignment
// without ModelCategory falls back to its Category. Every item must receive
// exactly one non-empty category.
func categorizeItems(ctx context.Context, ic ItemCategorizer, items []order.Item, allowed []string,
	overrides map[string]string,
) ([]splitter.Item, error) {
	assignments, err := ic.Categorize(ctx, items, allowed)
	if err != nil {
		return nil, err
	}
	cats := make([]string, len(items))
	for _, a := range assignments {
		if a.ItemIndex < 0 || a.ItemIndex >= len(items) {
			return nil, fmt.Errorf("categorizer returned an assignment for item %d of %d", a.ItemIndex, len(items))
		}
		base := a.ModelCategory
		if base == "" {
			base = a.Category
		}
		cats[a.ItemIndex] = categorizer.ApplyOverrides(base, overrides)
	}
	out := make([]splitter.Item, 0, len(items))
	for i, it := range items {
		if strings.TrimSpace(cats[i]) == "" {
			return nil, fmt.Errorf("categorizer returned no category for item %d", i)
		}
		out = append(out, splitter.Item{Name: it.Name, Category: cats[i], SubtotalCents: it.SubtotalCents})
	}
	return out, nil
}

// offeredCategories is the vocabulary handed to the categorizer: the YNAB
// names, then every category_overrides key that is not already among them
// (case-insensitive; the YNAB spelling wins), in sorted order. Offering the
// keys here means an override such as "Pet Supplies" -> "Pets" works however
// the categorizer was built; overrides are still applied afterwards by
// categorizeItems. The input slice is not modified.
func offeredCategories(allowed []string, overrides map[string]string) []string {
	out := append([]string(nil), allowed...)
	seen := make(map[string]bool, len(allowed)+len(overrides))
	for _, n := range allowed {
		seen[strings.ToLower(strings.TrimSpace(n))] = true
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		norm := strings.ToLower(strings.TrimSpace(k))
		if norm == "" || seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, k)
	}
	return out
}
