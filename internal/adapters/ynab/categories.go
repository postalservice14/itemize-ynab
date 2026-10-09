package ynab

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrCategoryNotFound is returned by FindCategoryByName when no category has the name.
	ErrCategoryNotFound = errors.New("ynab: category not found")
	// ErrCategoryAmbiguous is returned by FindCategoryByName when several categories share the name.
	ErrCategoryAmbiguous = errors.New("ynab: category name is ambiguous")
)

const (
	internalGroupName   = "internal master category"
	creditCardGroupName = "credit card payments"
	inflowCategoryName  = "inflow: ready to assign"
)

// AmbiguousCategoryError reports a name that matches categories in several
// groups. It matches ErrCategoryAmbiguous with errors.Is.
type AmbiguousCategoryError struct {
	Name   string
	Groups []string
}

// Error implements error.
func (e *AmbiguousCategoryError) Error() string {
	return fmt.Sprintf("ynab: category %q is ambiguous; it exists in groups: %s",
		e.Name, strings.Join(e.Groups, ", "))
}

// Is matches ErrCategoryAmbiguous.
func (e *AmbiguousCategoryError) Is(target error) bool { return target == ErrCategoryAmbiguous }

// EligibleCategories returns the categories the categorizer may assign items
// to, keeping the input order. It drops hidden and deleted categories, YNAB's
// internal categories (the "Internal Master Category" group and "Inflow: Ready
// to Assign"), the "Credit Card Payments" group and every category whose name
// is in excluded (ignoring case and surrounding space, in any group).
func EligibleCategories(all []Category, excluded ...string) []Category {
	skip := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		skip[normalize(name)] = struct{}{}
	}
	var out []Category
	for _, c := range all {
		if _, drop := skip[normalize(c.Name)]; !drop && isEligible(c) {
			out = append(out, c)
		}
	}
	return out
}

func isEligible(c Category) bool {
	if c.Hidden || c.Deleted {
		return false
	}
	switch normalize(c.GroupName) {
	case internalGroupName, creditCardGroupName:
		return false
	}
	return normalize(c.Name) != inflowCategoryName
}

// FindCategoryByName resolves a category name, ignoring case and surrounding
// space. It returns ErrCategoryNotFound when nothing matches and an
// *AmbiguousCategoryError when the name exists in more than one group. Pass
// EligibleCategories output to resolve only categories the categorizer can use.
func FindCategoryByName(cats []Category, name string) (Category, error) {
	want := normalize(name)
	var matches []Category
	for _, c := range cats {
		if normalize(c.Name) == want {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return Category{}, fmt.Errorf("%w: %q", ErrCategoryNotFound, name)
	case 1:
		return matches[0], nil
	}
	groups := make([]string, 0, len(matches))
	for _, m := range matches {
		groups = append(groups, m.GroupName)
	}
	return Category{}, &AmbiguousCategoryError{Name: name, Groups: groups}
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
