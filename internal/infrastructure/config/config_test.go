package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

const fullYAML = `
ynab:
  token: "${YNAB_TOKEN}"
  plan_id: "plan-9"
  flag_color: "blue"
  accounts:
    "0953": "acct-1"
  category_overrides:
    "Pet Supplies": "Pets"
  match_window:
    days_before: 3
    days_after: 7
  split_in_place: always
`

func TestParse_fullBlockAndEnvExpansion(t *testing.T) {
	cfg, err := config.Parse([]byte(fullYAML), env(map[string]string{"YNAB_TOKEN": "tok-abc"}))

	require.NoError(t, err)
	assert.Equal(t, "tok-abc", cfg.YNAB.Token.Reveal())
	assert.Equal(t, "plan-9", cfg.YNAB.PlanID)
	assert.Equal(t, "blue", cfg.YNAB.FlagColor)
	assert.Equal(t, map[string]string{"0953": "acct-1"}, cfg.YNAB.Accounts)
	assert.Equal(t, map[string]string{"Pet Supplies": "Pets"}, cfg.YNAB.CategoryOverrides)
	assert.Equal(t, 3, cfg.YNAB.MatchWindow.DaysBefore)
	assert.Equal(t, 7, cfg.YNAB.MatchWindow.DaysAfter)
	assert.Equal(t, "always", cfg.YNAB.SplitInPlace)
}

func TestParse_defaults(t *testing.T) {
	cfg, err := config.Parse([]byte("ynab:\n  token: abc\n"), env(nil))

	require.NoError(t, err)
	assert.Equal(t, "last-used", cfg.YNAB.PlanID)
	assert.Equal(t, "purple", cfg.YNAB.FlagColor)
	assert.Equal(t, 2, cfg.YNAB.MatchWindow.DaysBefore)
	assert.Equal(t, 10, cfg.YNAB.MatchWindow.DaysAfter)
	assert.Equal(t, "auto", cfg.YNAB.SplitInPlace)
}

func TestParse_explicitZeroWindowIsKept(t *testing.T) {
	cfg, err := config.Parse([]byte("ynab:\n  token: abc\n  match_window:\n    days_before: 0\n    days_after: 0\n"), env(nil))

	require.NoError(t, err)
	assert.Equal(t, 0, cfg.YNAB.MatchWindow.DaysBefore)
	assert.Equal(t, 0, cfg.YNAB.MatchWindow.DaysAfter)
}

func TestParse_missingEnvVarNamesVarNotValues(t *testing.T) {
	yaml := "ynab:\n  token: \"${YNAB_TOKEN}\"\n  plan_id: \"${PLAN}\"\n  flag_color: \"${COLOR}\"\n"

	_, err := config.Parse([]byte(yaml), env(map[string]string{"COLOR": "super-secret-value"}))

	require.ErrorIs(t, err, config.ErrInvalid)
	assert.Contains(t, err.Error(), "YNAB_TOKEN")
	assert.Contains(t, err.Error(), "PLAN")
	assert.NotContains(t, err.Error(), "COLOR")
	assert.NotContains(t, err.Error(), "super-secret-value")
}

func TestParse_emptyEnvVarCountsAsMissing(t *testing.T) {
	_, err := config.Parse([]byte("ynab:\n  token: \"${YNAB_TOKEN}\"\n"), env(map[string]string{"YNAB_TOKEN": ""}))

	require.ErrorIs(t, err, config.ErrInvalid)
	assert.Contains(t, err.Error(), "YNAB_TOKEN")
}

func TestParse_rejectsMalformedAndUnknownKeys(t *testing.T) {
	tests := map[string]string{ //nolint:gosec // YAML fixtures, not credentials
		"bad yaml":         "ynab: [unterminated",
		"unknown ynab key": "ynab:\n  token: a\n  splitinplace: auto\n",
		"missing block":    "other: 1\n",
		"empty token":      "ynab:\n  token: \"\"\n",
		"no token":         "ynab:\n  plan_id: x\n",
	}
	for name, yaml := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.Parse([]byte(yaml), env(nil))
			require.ErrorIs(t, err, config.ErrInvalid)
		})
	}
}

func TestParse_ignoresOtherTopLevelSections(t *testing.T) {
	_, err := config.Parse([]byte("ynab:\n  token: a\nother:\n  anything: 1\n"), env(nil))

	require.NoError(t, err)
}

func TestValidate_listsEveryProblem(t *testing.T) {
	yaml := `
ynab:
  token: abc
  flag_color: pink
  split_in_place: sometimes
  match_window:
    days_before: -1
    days_after: -5
  accounts:
    "12": "acct-1"
    "0953": ""
`
	_, err := config.Parse([]byte(yaml), env(nil))

	require.ErrorIs(t, err, config.ErrInvalid)
	for _, want := range []string{"flag_color", "pink", "split_in_place", "sometimes",
		"days_before", "days_after", `"12"`, `"0953"`} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestValidate_acceptsEveryAllowedValue(t *testing.T) {
	for _, mode := range []string{"auto", "always", "never"} {
		for _, color := range []string{"red", "orange", "yellow", "green", "blue", "purple"} {
			yaml := fmt.Sprintf("ynab:\n  token: a\n  split_in_place: %s\n  flag_color: %s\n", mode, color)
			_, err := config.Parse([]byte(yaml), env(nil))
			require.NoError(t, err, "%s/%s", mode, color)
		}
	}
}

func TestSecret_redactsEverywhere(t *testing.T) {
	cfg, err := config.Parse([]byte("ynab:\n  token: tok-abc\n"), env(nil))
	require.NoError(t, err)

	for _, out := range []string{
		fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg),
		fmt.Sprintf("%v", cfg.YNAB), fmt.Sprintf("%+v", *cfg), fmt.Sprint(cfg.YNAB.Token),
	} {
		assert.NotContains(t, out, "tok-abc")
	}
}

func TestLoad_readsFileAndReportsMissingFile(t *testing.T) {
	t.Setenv("YNAB_TOKEN", "tok-file")
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(fullYAML), 0o600))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "tok-file", cfg.YNAB.Token.Reveal())

	_, err = config.Load(filepath.Join(t.TempDir(), "nope.yaml"))
	require.ErrorIs(t, err, config.ErrInvalid)
}

func TestLoad_repoExampleConfigParses(t *testing.T) {
	t.Setenv("YNAB_TOKEN", "tok")

	cfg, err := config.Load(filepath.Join("..", "..", "..", "config.yaml"))

	require.NoError(t, err)
	assert.Equal(t, "last-used", cfg.YNAB.PlanID)
	assert.Equal(t, "Pets", cfg.YNAB.CategoryOverrides["Pet Supplies"])
}

func TestCrossCheck_listsAllBadEntriesInOneError(t *testing.T) {
	cfg, err := config.Parse([]byte(`
ynab:
  token: a
  accounts:
    "0001": good-acct
    "0002": bad-acct-1
    "0003": bad-acct-2
  category_overrides:
    "Pet Supplies": Pets
    "Candy": Sweets
    "Toys": Gifts
`), env(nil))
	require.NoError(t, err)
	findCategory := func(name string) error {
		switch name {
		case "Pets":
			return nil
		case "Gifts":
			return fmt.Errorf("category %q is ambiguous in groups: A, B", name)
		}
		return fmt.Errorf("category %q not found", name)
	}
	accountExists := func(id string) bool { return id == "good-acct" }

	err = cfg.YNAB.CrossCheck(findCategory, func(string) bool { return true }, accountExists)

	require.ErrorIs(t, err, config.ErrInvalid)
	msg := err.Error()
	for _, want := range []string{"Candy", "Sweets", "Toys", "Gifts", "ambiguous", "0002", "bad-acct-1", "0003", "bad-acct-2"} {
		assert.Contains(t, msg, want)
	}
	assert.NotContains(t, msg, "good-acct")
	assert.NotContains(t, msg, `"Pets"`)
	assert.Equal(t, 1, strings.Count(msg, "invalid configuration"), "one error, not a chain")
}

func TestCrossCheck_passesWhenEverythingExists(t *testing.T) {
	cfg, err := config.Parse([]byte("ynab:\n  token: a\n  accounts:\n    \"0001\": x\n  category_overrides:\n    A: B\n"), env(nil))
	require.NoError(t, err)

	err = cfg.YNAB.CrossCheck(func(string) error { return nil }, func(string) bool { return true }, func(string) bool { return true })

	assert.NoError(t, err)
}

func parseAt(t *testing.T, yaml, base, home string, vars map[string]string) (*config.Config, error) {
	t.Helper()
	return config.ParseWithBase([]byte(yaml), env(vars), base, home)
}

func TestParse_databaseAndWalmartDefaults(t *testing.T) {
	cfg, err := parseAt(t, "ynab:\n  token: a\n", filepath.FromSlash("/etc/iy"), filepath.FromSlash("/home/u"), nil)

	require.NoError(t, err)
	assert.Equal(t, filepath.FromSlash("/etc/iy/itemize-ynab.db"), cfg.Database.Path)
	assert.Equal(t, filepath.FromSlash("/home/u/.walmart-api/cookies.json"), cfg.Walmart.CookieFile)
}

func TestParse_databasePathResolution(t *testing.T) {
	base := filepath.FromSlash("/etc/iy")
	tests := map[string]struct{ yaml, want string }{
		"relative joins the config dir": {"database:\n  path: data/x.db\n", filepath.FromSlash("/etc/iy/data/x.db")},
		"absolute is kept":              {"database:\n  path: /var/lib/iy.db\n", filepath.FromSlash("/var/lib/iy.db")},
		"tilde expands":                 {"database:\n  path: ~/iy.db\n", filepath.FromSlash("/home/u/iy.db")},
		"env expands":                   {"database:\n  path: \"${DB_DIR}/iy.db\"\n", filepath.FromSlash("/srv/iy.db")},
		"empty means default":           {"database:\n  path: \"\"\n", filepath.FromSlash("/etc/iy/itemize-ynab.db")},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := parseAt(t, "ynab:\n  token: a\n"+tc.yaml, base, filepath.FromSlash("/home/u"), map[string]string{"DB_DIR": "/srv"})

			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Database.Path)
		})
	}
}

func TestParse_cookieFileResolution(t *testing.T) {
	cfg, err := parseAt(t, "ynab:\n  token: a\nwalmart:\n  cookie_file: ~/c/cookies.json\n", "/etc/iy", "/home/u", nil)
	require.NoError(t, err)
	assert.Equal(t, filepath.FromSlash("/home/u/c/cookies.json"), cfg.Walmart.CookieFile)

	cfg, err = parseAt(t, "ynab:\n  token: a\nwalmart:\n  cookie_file: rel/cookies.json\n", filepath.FromSlash("/etc/iy"), "/home/u", nil)
	require.NoError(t, err)
	assert.Equal(t, filepath.FromSlash("/etc/iy/rel/cookies.json"), cfg.Walmart.CookieFile)
}

func TestParse_newBlocksRejectBadInput(t *testing.T) {
	tests := map[string]string{
		"unknown database key":   "ynab:\n  token: a\ndatabase:\n  file: x.db\n",
		"unknown walmart key":    "ynab:\n  token: a\nwalmart:\n  cookies: x\n",
		"database not a map":     "ynab:\n  token: a\ndatabase: [x]\n",
		"other user's home":      "ynab:\n  token: a\nwalmart:\n  cookie_file: ~bob/c.json\n",
		"missing env var":        "ynab:\n  token: a\ndatabase:\n  path: \"${NOPE_DB}/x\"\n",
		"missing env in walmart": "ynab:\n  token: a\nwalmart:\n  cookie_file: \"${NOPE_CK}\"\n",
	}
	for name, yaml := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseAt(t, yaml, "/etc/iy", "/home/u", nil)

			require.ErrorIs(t, err, config.ErrInvalid)
		})
	}
}

func TestParse_tildeWithoutHomeNamesTheSetting(t *testing.T) {
	_, err := parseAt(t, "ynab:\n  token: a\n", "/etc/iy", "", nil)

	require.ErrorIs(t, err, config.ErrInvalid)
	assert.Contains(t, err.Error(), "walmart.cookie_file")
}

func TestParse_withoutBaseKeepsRelativePaths(t *testing.T) {
	cfg, err := config.Parse([]byte("ynab:\n  token: a\ndatabase:\n  path: x.db\nwalmart:\n  cookie_file: c.json\n"), env(nil))

	require.NoError(t, err)
	assert.Equal(t, "x.db", cfg.Database.Path)
	assert.Equal(t, "c.json", cfg.Walmart.CookieFile)
}

func TestLoad_resolvesAgainstConfigDirAndHome(t *testing.T) {
	t.Setenv("YNAB_TOKEN", "tok")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("ynab:\n  token: \"${YNAB_TOKEN}\"\n"), 0o600))

	cfg, err := config.Load(path)

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "itemize-ynab.db"), cfg.Database.Path)
	home, herr := os.UserHomeDir()
	require.NoError(t, herr)
	assert.Equal(t, filepath.Join(home, ".walmart-api", "cookies.json"), cfg.Walmart.CookieFile)
}

func TestParse_excludeCategories(t *testing.T) {
	cfg, err := config.Parse([]byte("ynab:\n  token: a\n  exclude_categories:\n    - Mortgage\n    - Tithe\n"), env(nil))

	require.NoError(t, err)
	assert.Equal(t, []string{"Mortgage", "Tithe"}, cfg.YNAB.ExcludeCategories)
}

func TestParse_excludeCategoriesDefaultsToNone(t *testing.T) {
	cfg, err := config.Parse([]byte("ynab:\n  token: a\n"), env(nil))

	require.NoError(t, err)
	assert.Empty(t, cfg.YNAB.ExcludeCategories)
}

func TestParse_excludeCategoriesBlankEntryRejected(t *testing.T) {
	_, err := config.Parse([]byte("ynab:\n  token: a\n  exclude_categories:\n    - Mortgage\n    - \"  \"\n"), env(nil))

	require.ErrorIs(t, err, config.ErrInvalid)
	assert.Contains(t, err.Error(), "exclude_categories")
}

func TestParseCookieFile_readsOnlyTheWalmartSetting(t *testing.T) {
	yaml := "ynab:\n  token: \"${YNAB_TOKEN}\"\n  split_in_place: bogus\nwalmart:\n  cookie_file: ~/c/cookies.json\n"

	got, err := config.ParseCookieFile([]byte(yaml), env(nil), "/etc/iy", "/home/u")

	require.NoError(t, err)
	assert.Equal(t, filepath.FromSlash("/home/u/c/cookies.json"), got)
}

func TestParseCookieFile_defaultsWithoutAWalmartSection(t *testing.T) {
	got, err := config.ParseCookieFile([]byte("ynab:\n  token: a\n"), env(nil), "/etc/iy", "/home/u")

	require.NoError(t, err)
	assert.Equal(t, filepath.FromSlash("/home/u/.walmart-api/cookies.json"), got)
}

func TestParseCookieFile_expandsAndResolvesRelativeToConfig(t *testing.T) {
	yaml := "walmart:\n  cookie_file: \"${CK_DIR}/cookies.json\"\n"

	got, err := config.ParseCookieFile([]byte(yaml), env(map[string]string{"CK_DIR": "rel"}), filepath.FromSlash("/etc/iy"), "/home/u")

	require.NoError(t, err)
	assert.Equal(t, filepath.FromSlash("/etc/iy/rel/cookies.json"), got)
}

func TestParseCookieFile_rejectsBadWalmartInput(t *testing.T) {
	tests := map[string]string{
		"malformed yaml":      "walmart: [\n",
		"unknown walmart key": "walmart:\n  cookies: x\n",
		"missing env var":     "walmart:\n  cookie_file: \"${NOPE_CK}\"\n",
		"other user's home":   "walmart:\n  cookie_file: ~bob/c.json\n",
	}
	for name, yaml := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.ParseCookieFile([]byte(yaml), env(nil), "/etc/iy", "/home/u")

			require.ErrorIs(t, err, config.ErrInvalid)
		})
	}
}

func TestLoadCookieFile_readsFileWithoutTheToken(t *testing.T) {
	t.Setenv("YNAB_TOKEN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("ynab:\n  token: \"${YNAB_TOKEN}\"\nwalmart:\n  cookie_file: c.json\n"), 0o600))

	got, err := config.LoadCookieFile(path)

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "c.json"), got)

	_, err = config.LoadCookieFile(filepath.Join(dir, "missing.yaml"))
	require.ErrorIs(t, err, config.ErrInvalid)
}

func TestParse_tipCategory(t *testing.T) {
	cfg, err := config.Parse([]byte("ynab:\n  token: a\n  tip_category: \"${TIP}\"\n"), env(map[string]string{"TIP": "Delivery"}))

	require.NoError(t, err)
	assert.Equal(t, "Delivery", cfg.YNAB.TipCategory)
}

func TestCrossCheck_tipCategoryMustBeUsable(t *testing.T) {
	cfg, err := config.Parse([]byte("ynab:\n  token: a\n  tip_category: Tips\n"), env(nil))
	require.NoError(t, err)
	notFound := func(name string) error { return fmt.Errorf("category %q not found", name) }

	err = cfg.YNAB.CrossCheck(notFound, func(string) bool { return true }, func(string) bool { return true })

	require.ErrorIs(t, err, config.ErrInvalid)
	assert.Contains(t, err.Error(), `ynab.tip_category "Tips"`)
}

func TestCrossCheck_noTipCategoryIsFine(t *testing.T) {
	cfg, err := config.Parse([]byte("ynab:\n  token: a\n"), env(nil))
	require.NoError(t, err)
	notFound := func(name string) error { return fmt.Errorf("category %q not found", name) }

	assert.NoError(t, cfg.YNAB.CrossCheck(notFound, func(string) bool { return true }, func(string) bool { return true }))
}
