// Package config loads and validates itemize-ynab's YAML configuration.
// Loading never touches the network; checks against live YNAB data are a
// separate step (YNAB.CrossCheck) that takes already-fetched information.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrInvalid is matched (errors.Is) by every configuration problem, so the CLI
// can map it to exit code 1.
var ErrInvalid = errors.New("invalid configuration")

const (
	defaultPlanID       = "last-used"
	defaultFlagColor    = "purple"
	defaultDaysBefore   = 2
	defaultDaysAfter    = 10
	defaultSplitInPlace = "auto"
)

// Config is the whole configuration file.
type Config struct {
	YNAB     YNAB
	Database Database
	Walmart  Walmart
}

// Database is the `database:` block.
type Database struct {
	// Path is the SQLite file. Relative paths are resolved against the config
	// file's directory (so cron runs do not depend on the working directory).
	Path string
}

// Walmart is the `walmart:` block.
type Walmart struct {
	// CookieFile is the Walmart cookie store written by `walmart import-curl`.
	// A leading ~ is expanded to the home directory.
	CookieFile string
}

// YNAB is the `ynab:` block.
type YNAB struct {
	Token             Secret
	PlanID            string
	FlagColor         string
	Accounts          map[string]string // card last 4 -> YNAB account ID
	CategoryOverrides map[string]string // categorizer name -> YNAB category name
	ExcludeCategories []string          // YNAB category names the categorizer may never use
	MatchWindow       MatchWindow
	SplitInPlace      string // auto | always | never
}

// MatchWindow is how far a YNAB transaction may sit from the order date.
type MatchWindow struct {
	DaysBefore int
	DaysAfter  int
}

// Secret is a value that must never be printed.
type Secret string

// String, GoString, Format and MarshalText all redact.
func (Secret) String() string               { return "REDACTED" }
func (Secret) GoString() string             { return "REDACTED" }
func (Secret) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte("REDACTED")) }
func (Secret) MarshalText() ([]byte, error) { return []byte("REDACTED"), nil }

// Reveal returns the underlying value.
func (s Secret) Reveal() string { return string(s) }

// Format keeps %v and %+v of a Config or YNAB from reaching the token, which
// fmt cannot redact on its own inside an unexported-method-free struct walk.
func (y YNAB) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "ynab{plan:%s accounts:%d overrides:%d split_in_place:%s token:REDACTED}",
		y.PlanID, len(y.Accounts), len(y.CategoryOverrides), y.SplitInPlace)
}

type rawDatabase struct {
	Path string `yaml:"path"`
}

type rawWalmart struct {
	CookieFile string `yaml:"cookie_file"`
}

type rawYNAB struct {
	Token             string            `yaml:"token"`
	PlanID            string            `yaml:"plan_id"`
	FlagColor         string            `yaml:"flag_color"`
	Accounts          map[string]string `yaml:"accounts"`
	CategoryOverrides map[string]string `yaml:"category_overrides"`
	ExcludeCategories []string          `yaml:"exclude_categories"`
	MatchWindow       struct {
		DaysBefore *int `yaml:"days_before"`
		DaysAfter  *int `yaml:"days_after"`
	} `yaml:"match_window"`
	SplitInPlace string `yaml:"split_in_place"`
}

// Load reads path and parses it using the process environment.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the config path is an operator-supplied argument
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %w", ErrInvalid, path, err)
	}
	home, _ := os.UserHomeDir() // an unknown home only matters if a path starts with ~
	return ParseWithBase(data, os.LookupEnv, filepath.Dir(path), home)
}

// Parse decodes YAML, expands ${VAR} references with lookup (an unset or empty
// variable is an error naming the variable, never its value), applies defaults
// and validates. Top-level sections other than `ynab` are ignored; unknown keys
// inside `ynab` are rejected so typos do not silently drop settings.
func Parse(data []byte, lookup func(string) (string, bool)) (*Config, error) {
	return parse(data, lookup, nil)
}

// ParseWithBase is Parse plus path resolution: a relative database.path is
// joined to baseDir and a leading ~ in walmart.cookie_file is replaced by
// homeDir. An empty baseDir leaves relative paths as written; an empty homeDir
// makes a leading ~ an error that names the setting.
func ParseWithBase(data []byte, lookup func(string) (string, bool), baseDir, homeDir string) (*Config, error) {
	return parse(data, lookup, &pathResolver{baseDir: baseDir, homeDir: homeDir})
}

func parse(data []byte, lookup func(string) (string, bool), res *pathResolver) (*Config, error) {
	top, err := decodeTop(data)
	if err != nil {
		return nil, err
	}
	raw, err := decodeSection[rawYNAB](top, "ynab", true)
	if err != nil {
		return nil, err
	}
	rawDB, err := decodeSection[rawDatabase](top, "database", false)
	if err != nil {
		return nil, err
	}
	rawWM, err := decodeSection[rawWalmart](top, "walmart", false)
	if err != nil {
		return nil, err
	}
	exp := &expander{lookup: lookup, missing: map[string]struct{}{}}
	y := YNAB{
		Token:             Secret(exp.expand(raw.Token)),
		PlanID:            exp.expand(raw.PlanID),
		FlagColor:         exp.expand(raw.FlagColor),
		Accounts:          exp.expandMap(raw.Accounts),
		CategoryOverrides: exp.expandMap(raw.CategoryOverrides),
		ExcludeCategories: raw.ExcludeCategories,
		SplitInPlace:      exp.expand(raw.SplitInPlace),
		MatchWindow: MatchWindow{
			DaysBefore: intOr(raw.MatchWindow.DaysBefore, defaultDaysBefore),
			DaysAfter:  intOr(raw.MatchWindow.DaysAfter, defaultDaysAfter),
		},
	}
	dbPath := exp.expand(rawDB.Path)
	cookieFile := exp.expand(rawWM.CookieFile)
	if err := exp.err(); err != nil {
		return nil, err
	}
	applyDefaults(&y)
	cfg := &Config{YNAB: y}
	if err := res.apply(cfg, dbPath, cookieFile); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func decodeTop(data []byte) (map[string]yaml.Node, error) {
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("%w: parse yaml: %w", ErrInvalid, err)
	}
	return top, nil
}

// decodeSection strictly decodes one top-level section; unknown keys inside it
// are rejected. An absent section is an error only when required.
func decodeSection[T any](top map[string]yaml.Node, name string, required bool) (T, error) {
	var raw T
	node, ok := top[name]
	if !ok {
		if required {
			return raw, fmt.Errorf("%w: missing top-level %q section", ErrInvalid, name)
		}
		return raw, nil
	}
	block, err := yaml.Marshal(&node)
	if err != nil {
		return raw, fmt.Errorf("%w: re-encode %s section: %w", ErrInvalid, name, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(block))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return raw, fmt.Errorf("%w: %s section: %w", ErrInvalid, name, err)
	}
	return raw, nil
}

func intOr(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}

func applyDefaults(y *YNAB) {
	if y.PlanID == "" {
		y.PlanID = defaultPlanID
	}
	if y.FlagColor == "" {
		y.FlagColor = defaultFlagColor
	}
	if y.SplitInPlace == "" {
		y.SplitInPlace = defaultSplitInPlace
	}
}

type expander struct {
	lookup  func(string) (string, bool)
	missing map[string]struct{}
}

func (e *expander) expand(s string) string {
	var out strings.Builder
	for {
		start := strings.Index(s, "${")
		if start < 0 {
			out.WriteString(s)
			return out.String()
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			out.WriteString(s)
			return out.String()
		}
		name := s[start+2 : start+end]
		out.WriteString(s[:start])
		if v, ok := e.lookup(name); ok && v != "" {
			out.WriteString(v)
		} else {
			e.missing[name] = struct{}{}
		}
		s = s[start+end+1:]
	}
}

func (e *expander) expandMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[e.expand(k)] = e.expand(v)
	}
	return out
}

func (e *expander) err() error {
	if len(e.missing) == 0 {
		return nil
	}
	names := make([]string, 0, len(e.missing))
	for n := range e.missing {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Errorf("%w: environment variable(s) not set or empty: %s", ErrInvalid, strings.Join(names, ", "))
}
