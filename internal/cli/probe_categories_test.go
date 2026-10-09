package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
)

// putSplitCategories decodes the first probe PUT and returns the category IDs
// of its subtransactions in order, plus their amounts.
func putSplitCategories(t *testing.T, srv *ynabtest.Server) ([]string, []int64) {
	t.Helper()
	puts := srv.Calls("PUT", txnPath)
	require.NotEmpty(t, puts, "expected a probe PUT")
	var body struct {
		Transaction struct {
			SubTransactions []struct {
				Amount     int64  `json:"amount"`
				CategoryID string `json:"category_id"`
			} `json:"subtransactions"`
		} `json:"transaction"`
	}
	require.NoError(t, json.Unmarshal(puts[0].Body, &body))
	var ids []string
	var amounts []int64
	for _, s := range body.Transaction.SubTransactions {
		ids = append(ids, s.CategoryID)
		amounts = append(amounts, s.Amount)
	}
	return ids, amounts
}

func TestProbe_categoriesFlag_dryRunShowsChosenCategoriesAndAmountsWithoutWriting(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv, []ynabtest.Response{okTxn(plainTxn(""))})

	r := run(t, srv, "ynab", "probe-split", "-categories", "pets, GROCERIES", "t1")

	assert.Equal(t, 1, r.code, "refuses without -yes")
	assert.Contains(t, r.stdout, `will split into "Pets" and "Groceries"`)
	assert.Equal(t, 2, strings.Count(r.stdout, "-92915"), "both parts are half of -185830")
	assert.Equal(t, 0, srv.WriteCount())
}

func TestProbe_categoriesFlag_dryRunShowsExactOddAmountSplit(t *testing.T) {
	srv := ynabtest.New(t)
	odd := plainTxn("")
	odd["amount"] = -185831
	scriptProbe(srv, []ynabtest.Response{okTxn(odd)})

	r := run(t, srv, "ynab", "probe-split", "-categories", "Groceries,Pets", "t1")

	assert.Contains(t, r.stdout, "-92915")
	assert.Contains(t, r.stdout, "-92916")
}

func TestProbe_categoriesFlag_usesTheChosenCategoriesInOrderForTheWrite(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv,
		[]ynabtest.Response{
			okTxn(plainTxn("")),
			okTxn(splitTxn("[itemize:probe]")),
			okTxn(plainTxn("")),
		},
		okTxn(splitTxn("[itemize:probe]")),
		okTxn(plainTxn("")),
	)

	r := run(t, srv, "ynab", "probe-split", "-yes", "-categories", "Pets,Groceries", "t1")

	require.Equal(t, 0, r.code, r.stderr)
	ids, amounts := putSplitCategories(t, srv)
	assert.Equal(t, []string{"c3", "c1"}, ids, "Pets (c3) first, Groceries (c1) second")
	require.Len(t, amounts, 2)
	assert.Equal(t, int64(-185830), amounts[0]+amounts[1])
}

func TestProbe_categoriesFlag_withoutFlagKeepsTheDefaultFirstTwoEligible(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv, []ynabtest.Response{okTxn(plainTxn(""))})

	r := run(t, srv, "ynab", "probe-split", "t1")

	assert.Contains(t, r.stdout, `will split into "Groceries" and "Pets"`)
}

func TestProbe_categoriesFlag_badSpecsFailBeforeAnyNetworkCall(t *testing.T) {
	for name, spec := range map[string]string{
		"one name":      "Pets",
		"three names":   "Pets,Groceries,Other",
		"empty element": "Pets,",
		"blank element": "Pets,  ",
		"same twice":    "Pets,pets",
	} {
		t.Run(name, func(t *testing.T) {
			srv := ynabtest.New(t)

			r := run(t, srv, "ynab", "probe-split", "-yes", "-categories", spec, "t1")

			assert.Equal(t, 1, r.code)
			assert.Contains(t, r.stderr, "-categories")
			assert.Empty(t, srv.Requests(), "a malformed spec must not reach the network")
		})
	}
}

func TestProbe_categoriesFlag_unknownNameIsRejectedWithHintAndNoWrite(t *testing.T) {
	srv := ynabtest.New(t)
	scriptProbe(srv, []ynabtest.Response{okTxn(plainTxn(""))})

	r := run(t, srv, "ynab", "probe-split", "-yes", "-categories", "Groceries,Nonexistent", "t1")

	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "Nonexistent")
	assert.Contains(t, r.stderr, "ynab categories -eligible")
	assert.Equal(t, 0, srv.WriteCount())
}

func TestProbe_categoriesFlag_ineligibleCategoriesAreRejected(t *testing.T) {
	for name, spec := range map[string]string{
		"hidden":            "Groceries,Old Stuff",
		"internal inflow":   "Groceries,Inflow: Ready to Assign",
		"credit card group": "Groceries,Visa",
		"deleted group":     "Groceries,Gone",
	} {
		t.Run(name, func(t *testing.T) {
			srv := ynabtest.New(t)
			scriptProbe(srv, []ynabtest.Response{okTxn(plainTxn(""))})

			r := run(t, srv, "ynab", "probe-split", "-yes", "-categories", spec, "t1")

			assert.Equal(t, 1, r.code)
			assert.Equal(t, 0, srv.WriteCount())
		})
	}
}

func TestProbe_categoriesFlag_ambiguousNameListsTheGroupsAndDoesNotWrite(t *testing.T) {
	srv := ynabtest.New(t)
	srv.On("GET", "/plans/plan-1/categories", ynabtest.OK(ynabtest.CategoriesData(1,
		ynabtest.Group{ID: "g1", Name: "Family", Categories: []ynabtest.Cat{{ID: "c1", Name: "Gifts"}, {ID: "c2", Name: "Fun"}}},
		ynabtest.Group{ID: "g2", Name: "Holidays", Categories: []ynabtest.Cat{{ID: "c3", Name: "Gifts"}}},
	)))
	srv.On("GET", txnPath, okTxn(plainTxn("")))

	r := run(t, srv, "ynab", "probe-split", "-yes", "-categories", "Gifts,Fun", "t1")

	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "Family")
	assert.Contains(t, r.stderr, "Holidays")
	assert.Equal(t, 0, srv.WriteCount())
}
