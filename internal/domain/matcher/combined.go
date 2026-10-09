package matcher

import (
	"math/bits"
	"slices"

	"github.com/postalservice14/itemize-ynab/internal/domain/memo"
)

// MaxCombinedCharges bounds the charges MatchCombined searches: every subset
// is tried, so the cost doubles with each charge.
const MaxCombinedCharges = 12

// Group is a set of an order's charges that one transaction pays together.
// Charges are indices into the slice given to MatchCombined, ascending.
type Group struct {
	Txn     Txn
	Charges []int
}

// fit is one transaction whose amount equals one subset of the charges.
type fit struct {
	txn  int
	mask uint
}

// MatchCombined finds transactions that pay two or more of one order's charges
// at once: the bank can post several Walmart charges as a single amount. The
// charges must be the order's still-unmatched ones; single matches are Match's
// job.
//
// A transaction is a candidate on the same terms as in Match except for the
// amount, which must equal the sum of a subset of at least two charges, with
// the transaction date inside every member's window and every member allowed
// in its account. Only unambiguous fits are returned: the transaction fits
// exactly one subset, that subset fits no other transaction, and no member
// belongs to another fit. Anything else is left unmatched. Groups are ordered
// by their first charge. claimed is not mutated.
func MatchCombined(charges []Charge, txns []Txn, claimed map[string]bool, opts Options) []Group {
	n := len(charges)
	if n < 2 || n > MaxCombinedCharges {
		return nil
	}
	opts = withDefaults(opts)
	sums := subsetSums(charges)
	var fits []fit
	for ti, t := range txns {
		if !combinedCandidate(t, claimed, opts) {
			continue
		}
		for mask := uint(1); mask < uint(len(sums)); mask++ {
			if bits.OnesCount(mask) >= 2 && sums[mask]*milliPerCent == -t.AmountMilli &&
				membersFit(charges, mask, t, opts) {
				fits = append(fits, fit{txn: ti, mask: mask})
			}
		}
	}
	return unambiguous(fits, txns, n)
}

// subsetSums returns the cents total of every subset, indexed by bit mask.
func subsetSums(charges []Charge) []int64 {
	sums := make([]int64, 1<<len(charges))
	for mask := 1; mask < len(sums); mask++ {
		low := bits.TrailingZeros(uint(mask))
		sums[mask] = sums[mask&(mask-1)] + charges[low].AmountCents
	}
	return sums
}

func combinedCandidate(t Txn, claimed map[string]bool, opts Options) bool {
	switch {
	case t.Deleted, t.IsTransfer, t.IsSplit, t.AmountMilli >= 0:
		return false
	case memo.HasMarker(t.Memo), claimed[t.ID]:
		return false
	}
	return opts.PayeeRE.MatchString(t.PayeeName) || opts.PayeeRE.MatchString(t.ImportPayeeName)
}

func membersFit(charges []Charge, mask uint, t Txn, opts Options) bool {
	for i, c := range charges {
		if mask&(1<<i) != 0 && (!accountOK(c, t) || !inWindow(c.Date, t.Date, opts)) {
			return false
		}
	}
	return true
}

// unambiguous keeps the fits whose transaction and subset each appear once
// and whose members appear in no other kept fit.
func unambiguous(fits []fit, txns []Txn, n int) []Group {
	perTxn := map[int]int{}
	perMask := map[uint]int{}
	for _, f := range fits {
		perTxn[f.txn]++
		perMask[f.mask]++
	}
	var kept []fit
	memberUse := make([]int, n)
	for _, f := range fits {
		if perTxn[f.txn] != 1 || perMask[f.mask] != 1 {
			continue
		}
		kept = append(kept, f)
		for i := range n {
			if f.mask&(1<<i) != 0 {
				memberUse[i]++
			}
		}
	}
	var groups []Group
	for _, f := range kept {
		g := Group{Txn: txns[f.txn]}
		shared := false
		for i := range n {
			if f.mask&(1<<i) != 0 {
				g.Charges = append(g.Charges, i)
				shared = shared || memberUse[i] > 1
			}
		}
		if !shared {
			groups = append(groups, g)
		}
	}
	sortByFirstCharge(groups)
	return groups
}

func sortByFirstCharge(groups []Group) {
	slices.SortFunc(groups, func(a, b Group) int { return a.Charges[0] - b.Charges[0] })
}
