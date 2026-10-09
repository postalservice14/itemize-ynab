package matcher

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func chargeOf(cents int64, d int) Charge {
	return Charge{Key: fmt.Sprintf("walmart:1:%d:1", cents), AmountCents: cents, Date: day(d)}
}

func txnOf(id string, cents int64, d int, mutate ...func(*Txn)) Txn {
	return txn(id, d, append([]func(*Txn){func(x *Txn) { x.AmountMilli = -cents * milliPerCent }}, mutate...)...)
}

func TestMatchCombined_findsTheChargesOneTransactionPays(t *testing.T) {
	// The bank posted two of the order's charges as one transaction.
	charges := []Charge{chargeOf(9021, 3), chargeOf(570, 5), chargeOf(751, 4)}
	txns := []Txn{txnOf("bank", 9772, 6)}

	got := MatchCombined(charges, txns, map[string]bool{}, DefaultOptions())

	assert.Equal(t, []Group{{Txn: txns[0], Charges: []int{0, 2}}}, got)
}

func TestMatchCombined_severalGroupsInOneOrder(t *testing.T) {
	charges := []Charge{chargeOf(1422, 1), chargeOf(804, 1), chargeOf(10346, 2), chargeOf(8646, 2), chargeOf(500, 2), chargeOf(700, 2)}
	txns := []Txn{txnOf("big", 21218, 4), txnOf("small", 1200, 4)}

	got := MatchCombined(charges, txns, map[string]bool{}, DefaultOptions())

	assert.Equal(t, []Group{
		{Txn: txns[0], Charges: []int{0, 1, 2, 3}},
		{Txn: txns[1], Charges: []int{4, 5}},
	}, got)
}

func TestMatchCombined_noGroup(t *testing.T) {
	two := []Charge{chargeOf(500, 10), chargeOf(700, 10)}
	tests := []struct {
		name    string
		charges []Charge
		txns    []Txn
		claimed map[string]bool
	}{
		{"one charge is never a group", two, []Txn{txnOf("a", 500, 10)}, nil},
		{"sum differs by a cent", two, []Txn{txnOf("a", 1201, 10)}, nil},
		{"wrong payee", two, []Txn{txnOf("a", 1200, 10, func(x *Txn) { x.PayeeName = "Target" })}, nil},
		{"deleted", two, []Txn{txnOf("a", 1200, 10, func(x *Txn) { x.Deleted = true })}, nil},
		{"transfer", two, []Txn{txnOf("a", 1200, 10, func(x *Txn) { x.IsTransfer = true })}, nil},
		{"already split", two, []Txn{txnOf("a", 1200, 10, func(x *Txn) { x.IsSplit = true })}, nil},
		{"marked memo", two, []Txn{txnOf("a", 1200, 10, func(x *Txn) { x.Memo = "[itemize:walmart:9:1:1]" })}, nil},
		{"claimed", two, []Txn{txnOf("a", 1200, 10)}, map[string]bool{"a": true}},
		{"one member outside its window", []Charge{chargeOf(500, 10), chargeOf(700, 1)}, []Txn{txnOf("a", 1200, 12)}, nil},
		{
			"one member in another account",
			[]Charge{chargeOf(500, 10), {Key: "k", AmountCents: 700, Date: day(10), AccountID: "acct-2"}},
			[]Txn{txnOf("a", 1200, 10)}, nil,
		},
		{
			"two subsets fit the transaction",
			[]Charge{chargeOf(500, 10), chargeOf(500, 10), chargeOf(700, 10)},
			[]Txn{txnOf("a", 1200, 10)}, nil,
		},
		{"two transactions fit the subset", two, []Txn{txnOf("a", 1200, 10), txnOf("b", 1200, 11)}, nil},
		{
			"fits share a charge",
			[]Charge{chargeOf(100, 10), chargeOf(200, 10), chargeOf(400, 10)},
			[]Txn{txnOf("a", 300, 10), txnOf("b", 600, 10)}, nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claimed := tc.claimed
			if claimed == nil {
				claimed = map[string]bool{}
			}
			assert.Empty(t, MatchCombined(tc.charges, tc.txns, claimed, DefaultOptions()))
		})
	}
}

func TestMatchCombined_tooManyChargesIsNotSearched(t *testing.T) {
	charges := make([]Charge, MaxCombinedCharges+1)
	for i := range charges {
		charges[i] = chargeOf(100, 10)
	}
	charges[0].AmountCents, charges[1].AmountCents = 1, 2

	assert.Empty(t, MatchCombined(charges, []Txn{txnOf("a", 3, 10)}, map[string]bool{}, DefaultOptions()))
}

func TestMatchCombined_doesNotMutateClaimed(t *testing.T) {
	claimed := map[string]bool{}

	MatchCombined([]Charge{chargeOf(500, 10), chargeOf(700, 10)}, []Txn{txnOf("a", 1200, 10)}, claimed, DefaultOptions())

	assert.Empty(t, claimed)
}
