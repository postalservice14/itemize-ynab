package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	validSplitModes = []string{"auto", "always", "never"}
	validFlagColors = []string{"red", "orange", "yellow", "green", "blue", "purple"}
	cardLast4       = regexp.MustCompile(`^[0-9]{4}$`)
)

// ValidationError lists every problem found, in one error. It matches
// ErrInvalid with errors.Is.
type ValidationError struct {
	Problems []string
}

// Error implements error.
func (e *ValidationError) Error() string {
	return ErrInvalid.Error() + ":\n  - " + strings.Join(e.Problems, "\n  - ")
}

// Is matches ErrInvalid.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

func newValidationError(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: problems}
}

// Validate checks the config without any network access. It reports every
// problem at once.
func (c *Config) Validate() error {
	y := c.YNAB
	var p []string
	if y.Token.Reveal() == "" {
		p = append(p, "ynab.token is required (set it to ${YNAB_TOKEN})")
	}
	if !contains(validSplitModes, y.SplitInPlace) {
		p = append(p, fmt.Sprintf("ynab.split_in_place %q must be one of %s", y.SplitInPlace, strings.Join(validSplitModes, ", ")))
	}
	if !contains(validFlagColors, y.FlagColor) {
		p = append(p, fmt.Sprintf("ynab.flag_color %q must be one of %s", y.FlagColor, strings.Join(validFlagColors, ", ")))
	}
	if y.MatchWindow.DaysBefore < 0 {
		p = append(p, fmt.Sprintf("ynab.match_window.days_before %d must not be negative", y.MatchWindow.DaysBefore))
	}
	if y.MatchWindow.DaysAfter < 0 {
		p = append(p, fmt.Sprintf("ynab.match_window.days_after %d must not be negative", y.MatchWindow.DaysAfter))
	}
	for _, card := range sortedKeys(y.Accounts) {
		if !cardLast4.MatchString(card) {
			p = append(p, fmt.Sprintf("ynab.accounts key %q must be the card's last 4 digits", card))
		}
		if strings.TrimSpace(y.Accounts[card]) == "" {
			p = append(p, fmt.Sprintf("ynab.accounts[%q] needs a YNAB account ID", card))
		}
	}
	return newValidationError(p)
}

// CrossCheck verifies the config against live YNAB data fetched by the caller:
// findCategory must return nil when the named category is usable (exists among
// the eligible categories and is unambiguous) and otherwise an error saying
// why; accountExists reports whether an account ID is in the plan. Every bad
// override and account is listed in one error.
func (y YNAB) CrossCheck(findCategory func(name string) error, accountExists func(id string) bool) error {
	var p []string
	for _, from := range sortedKeys(y.CategoryOverrides) {
		to := y.CategoryOverrides[from]
		if err := findCategory(to); err != nil {
			p = append(p, fmt.Sprintf("ynab.category_overrides[%q] -> %q: %v", from, to, err))
		}
	}
	for _, card := range sortedKeys(y.Accounts) {
		if id := y.Accounts[card]; !accountExists(id) {
			p = append(p, fmt.Sprintf("ynab.accounts[%q] -> account ID %q does not exist in the plan", card, id))
		}
	}
	return newValidationError(p)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
