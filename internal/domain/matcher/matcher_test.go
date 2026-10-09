package matcher

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func day(d int) time.Time {
	return time.Date(2026, time.March, d, 0, 0, 0, 0, time.UTC)
}

func baseCharge() Charge {
	return Charge{Key: "walmart:1:4500:1", AmountCents: 4500, Date: day(10)}
}

// txn builds a matching candidate that tests then perturb.
func txn(id string, d int, mutate ...func(*Txn)) Txn {
	t := Txn{ID: id, AccountID: "acct-1", PayeeName: "Walmart", AmountMilli: -45000, Date: day(d)}
	for _, m := range mutate {
		m(&t)
	}
	return t
}

func TestMatch_filters(t *testing.T) {
	tests := []struct {
		name   string
		charge func(*Charge)
		txn    Txn
		match  bool
	}{
		{"plain match", nil, txn("a", 10), true},
		{"wrong payee", nil, txn("a", 10, func(x *Txn) { x.PayeeName = "Target" }), false},
		{"import payee only", nil, txn("a", 10, func(x *Txn) { x.PayeeName = "Groceries"; x.ImportPayeeName = "WM SUPERCENTER #123" }), true},
		{"wmt import payee", nil, txn("a", 10, func(x *Txn) { x.PayeeName = ""; x.ImportPayeeName = "WMT PLUS" }), true},
		{"walmart variants", nil, txn("a", 10, func(x *Txn) { x.PayeeName = "WAL-MART #55" }), true},
		{"wmt substring not a word", nil, txn("a", 10, func(x *Txn) { x.PayeeName = "SWMTX" }), false},
		{"deleted", nil, txn("a", 10, func(x *Txn) { x.Deleted = true }), false},
		{"transfer", nil, txn("a", 10, func(x *Txn) { x.IsTransfer = true }), false},
		{"already split", nil, txn("a", 10, func(x *Txn) { x.IsSplit = true }), false},
		{"marked memo", nil, txn("a", 10, func(x *Txn) { x.Memo = "x [itemize:walmart:9:1:1]" }), false},
		{"amount off by one milliunit", nil, txn("a", 10, func(x *Txn) { x.AmountMilli = -45001 }), false},
		{"positive amount", nil, txn("a", 10, func(x *Txn) { x.AmountMilli = 45000 }), false},
		{"window lower boundary inclusive", nil, txn("a", 8), true},
		{"window below lower boundary", nil, txn("a", 7), false},
		{"window upper boundary inclusive", nil, txn("a", 20), true},
		{"window above upper boundary", nil, txn("a", 21), false},
		{"account filter matches", func(c *Charge) { c.AccountID = "acct-1" }, txn("a", 10), true},
		{"account filter excludes", func(c *Charge) { c.AccountID = "acct-2" }, txn("a", 10), false},
		{"time of day ignored", nil, txn("a", 10, func(x *Txn) { x.Date = time.Date(2026, 3, 20, 23, 59, 0, 0, time.UTC) }), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := baseCharge()
			if tc.charge != nil {
				tc.charge(&c)
			}
			res := Match(c, []Txn{tc.txn}, nil, DefaultOptions())
			if tc.match {
				assert.Equal(t, Matched, res.Status, res.Reason)
				assert.Equal(t, tc.txn.ID, res.Txn.ID)
				return
			}
			assert.Equal(t, NoMatch, res.Status)
			assert.NotEmpty(t, res.Reason)
		})
	}
}

func TestMatch_claimed(t *testing.T) {
	claimed := map[string]bool{"a": true}
	res := Match(baseCharge(), []Txn{txn("a", 10)}, claimed, DefaultOptions())
	assert.Equal(t, NoMatch, res.Status)
	assert.Equal(t, map[string]bool{"a": true}, claimed, "Match must not mutate claimed")
}

func TestMatch_doesNotMutateClaimedOnSuccess(t *testing.T) {
	claimed := map[string]bool{}
	res := Match(baseCharge(), []Txn{txn("a", 10)}, claimed, DefaultOptions())
	require.Equal(t, Matched, res.Status)
	assert.Empty(t, claimed)
}

func TestMatch_closestDateWins(t *testing.T) {
	txns := []Txn{txn("far", 18), txn("near", 11), txn("before", 7+1)}
	res := Match(baseCharge(), txns, nil, DefaultOptions())
	require.Equal(t, Matched, res.Status)
	assert.Equal(t, "near", res.Txn.ID)
}

func TestMatch_closestDateConsidersBothDirections(t *testing.T) {
	// 2 days before is closer than 3 days after.
	res := Match(baseCharge(), []Txn{txn("after", 13), txn("before", 8)}, nil, DefaultOptions())
	require.Equal(t, Matched, res.Status)
	assert.Equal(t, "before", res.Txn.ID)
}

func TestMatch_sameAccountTieLowestID(t *testing.T) {
	txns := []Txn{txn("zzz", 12), txn("bbb", 12), txn("mmm", 12)}
	res := Match(baseCharge(), txns, nil, DefaultOptions())
	require.Equal(t, Matched, res.Status)
	assert.Equal(t, "bbb", res.Txn.ID)
}

func TestMatch_sameAccountTieEitherSideOfDate(t *testing.T) {
	// Same absolute distance (2 days), opposite directions, same account.
	res := Match(baseCharge(), []Txn{txn("b", 12), txn("a", 8)}, nil, DefaultOptions())
	require.Equal(t, Matched, res.Status)
	assert.Equal(t, "a", res.Txn.ID)
}

func TestMatch_crossAccountTieIsAmbiguous(t *testing.T) {
	other := func(x *Txn) { x.AccountID = "acct-2" }
	txns := []Txn{txn("a", 12), txn("b", 12, other), txn("far", 18)}
	res := Match(baseCharge(), txns, nil, DefaultOptions())
	assert.Equal(t, Ambiguous, res.Status)
	assert.Empty(t, res.Txn.ID)
	assert.Len(t, res.Candidates, 2)
	assert.NotEmpty(t, res.Reason)
}

func TestMatch_closerCrossAccountCandidateIsNotAmbiguous(t *testing.T) {
	other := func(x *Txn) { x.AccountID = "acct-2" }
	res := Match(baseCharge(), []Txn{txn("a", 11), txn("b", 12, other)}, nil, DefaultOptions())
	require.Equal(t, Matched, res.Status)
	assert.Equal(t, "a", res.Txn.ID)
}

func TestMatch_accountFilterResolvesCrossAccountTie(t *testing.T) {
	other := func(x *Txn) { x.AccountID = "acct-2" }
	c := baseCharge()
	c.AccountID = "acct-2"
	res := Match(c, []Txn{txn("a", 12), txn("b", 12, other)}, nil, DefaultOptions())
	require.Equal(t, Matched, res.Status)
	assert.Equal(t, "b", res.Txn.ID)
}

func TestMatch_noCandidates(t *testing.T) {
	res := Match(baseCharge(), nil, nil, DefaultOptions())
	assert.Equal(t, NoMatch, res.Status)
	assert.NotEmpty(t, res.Reason)
	assert.Empty(t, res.Candidates)
}

func TestMatch_duplicateChargesClaimDifferentTransactions(t *testing.T) {
	txns := []Txn{txn("t1", 10), txn("t2", 10)}
	claimed := map[string]bool{}

	first := Match(baseCharge(), txns, claimed, DefaultOptions())
	require.Equal(t, Matched, first.Status)
	Claim(claimed, first.Txn.ID)

	second := Match(baseCharge(), txns, claimed, DefaultOptions())
	require.Equal(t, Matched, second.Status)
	assert.NotEqual(t, first.Txn.ID, second.Txn.ID)
	Claim(claimed, second.Txn.ID)

	third := Match(baseCharge(), txns, claimed, DefaultOptions())
	assert.Equal(t, NoMatch, third.Status)
}

func TestMatch_duplicateChargesWithOnlyOneTransaction(t *testing.T) {
	txns := []Txn{txn("only", 10)}
	claimed := map[string]bool{}

	first := Match(baseCharge(), txns, claimed, DefaultOptions())
	require.Equal(t, Matched, first.Status)
	Claim(claimed, first.Txn.ID)

	second := Match(baseCharge(), txns, claimed, DefaultOptions())
	assert.Equal(t, NoMatch, second.Status)
	assert.NotEmpty(t, second.Reason)
}

func TestMatch_customOptions(t *testing.T) {
	opts := Options{DaysBefore: 0, DaysAfter: 1, PayeeRE: regexp.MustCompile(`(?i)costco`)}
	costco := txn("c", 11, func(x *Txn) { x.PayeeName = "Costco" })
	assert.Equal(t, Matched, Match(baseCharge(), []Txn{costco}, nil, opts).Status)
	assert.Equal(t, NoMatch, Match(baseCharge(), []Txn{txn("w", 10)}, nil, opts).Status)
	assert.Equal(t, NoMatch, Match(baseCharge(), []Txn{txn("early", 9, func(x *Txn) { x.PayeeName = "Costco" })}, nil, opts).Status)
}

func TestMatch_nilPayeeREUsesDefault(t *testing.T) {
	opts := Options{DaysBefore: 2, DaysAfter: 10}
	assert.Equal(t, Matched, Match(baseCharge(), []Txn{txn("a", 10)}, nil, opts).Status)
}

func TestMatch_dateComparedByCalendarDay(t *testing.T) {
	est := time.FixedZone("EST", -5*3600)
	// 23:30 local on the 20th is still the 20th, though it is the 21st in UTC.
	late := txn("a", 10, func(x *Txn) { x.Date = time.Date(2026, 3, 20, 23, 30, 0, 0, est) })
	assert.Equal(t, Matched, Match(baseCharge(), []Txn{late}, nil, DefaultOptions()).Status)

	c := baseCharge()
	c.Date = time.Date(2026, 3, 10, 23, 0, 0, 0, est)
	assert.Equal(t, Matched, Match(c, []Txn{txn("b", 20)}, nil, DefaultOptions()).Status)
}

func TestDefaultPayeeRE(t *testing.T) {
	re := DefaultPayeeRE()
	for _, s := range []string{"Walmart", "WALMART.COM", "wal-mart", "Wal Mart", "WM Supercenter", "WMT"} {
		assert.True(t, re.MatchString(s), s)
	}
	for _, s := range []string{"Target", "Kroger", "SWMTX", ""} {
		assert.False(t, re.MatchString(s), s)
	}
}

func TestHasAmountInAccount(t *testing.T) {
	c := baseCharge()
	c.AccountID = "acct-1"
	other := func(x *Txn) { x.AccountID = "acct-2" }
	tests := []struct {
		name string
		txn  Txn
		want bool
	}{
		{"any payee counts", txn("a", 10, func(x *Txn) { x.PayeeName = "Target" }), true},
		{"already split still counts", txn("a", 10, func(x *Txn) { x.IsSplit = true }), true},
		{"marked still counts", txn("a", 10, func(x *Txn) { x.Memo = "[itemize:k]" }), true},
		{"deleted ignored", txn("a", 10, func(x *Txn) { x.Deleted = true }), false},
		{"other account ignored", txn("a", 10, other), false},
		{"different amount", txn("a", 10, func(x *Txn) { x.AmountMilli = -45010 }), false},
		{"outside window", txn("a", 25), false},
		{"window boundary", txn("a", 20), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, HasAmountInAccount(c, []Txn{tc.txn}, DefaultOptions()))
		})
	}
	assert.False(t, HasAmountInAccount(c, nil, DefaultOptions()))
}

func TestHasAmountInAccount_emptyAccountMeansUnrestricted(t *testing.T) {
	c := baseCharge()
	assert.True(t, HasAmountInAccount(c, []Txn{txn("a", 10)}, DefaultOptions()))
}

func TestStatusString(t *testing.T) {
	assert.Equal(t, "matched", Matched.String())
	assert.Equal(t, "ambiguous", Ambiguous.String())
	assert.Equal(t, "no_match", NoMatch.String())
}
