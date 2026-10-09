// Package allocator splits an integer amount across weights using
// largest-remainder rounding, so the parts always sum exactly to the total.
//
// All math is integer. The multiply-then-divide step uses a 128-bit
// intermediate (math/bits), so it cannot overflow for any int64 inputs; the
// only overflow guarded against is the sum of the weights, which is an error.
package allocator

import (
	"errors"
	"fmt"
	"math/bits"
	"sort"
)

// Errors returned by Allocate. Use errors.Is to match.
var (
	ErrNegativeTotal  = errors.New("allocator: total must not be negative")
	ErrNegativeWeight = errors.New("allocator: weights must not be negative")
	ErrNoWeights      = errors.New("allocator: no weights to allocate across")
	ErrWeightOverflow = errors.New("allocator: sum of weights overflows int64")
)

// Allocate splits total across weights proportionally and returns one part per
// weight; the parts sum exactly to total and none is negative.
//
// Each part starts as floor(total*weight/sum). The leftover units (always
// fewer than the number of non-zero weights) go one each to the entries with
// the largest fractional remainders. Ties on equal remainders are broken by
// LOWEST INDEX, which makes the result deterministic. A zero-weight entry has
// a zero remainder, so it never receives a leftover unit while any weight is
// non-zero.
//
// If every weight is zero, the total is split evenly using the same rule
// (equal weights, so the lowest indexes receive the extra units). Callers that
// must not silently spread over meaningless weights should check first.
//
// total == 0 yields all zeros (an empty weight slice yields an empty result).
// A negative total or weight, an empty weight slice with a non-zero total, or
// a weight sum exceeding int64 is an error.
func Allocate(total int64, weights []int64) ([]int64, error) {
	if total < 0 {
		return nil, ErrNegativeTotal
	}
	if total != 0 && len(weights) == 0 {
		return nil, ErrNoWeights
	}
	w, sum, err := normalize(weights)
	if err != nil {
		return nil, err
	}
	parts := make([]int64, len(w))
	if total == 0 {
		return parts, nil
	}
	rems := make([]uint64, len(w))
	var assigned int64
	for i, wi := range w {
		// total, wi and sum are all non-negative (validated above), so the
		// unsigned conversions are lossless.
		hi, lo := bits.Mul64(uint64(total), uint64(wi)) //nolint:gosec // non-negative
		// hi < sum because wi <= sum and total < 2^64, so Div64 cannot panic.
		q, r := bits.Div64(hi, lo, uint64(sum)) //nolint:gosec // non-negative
		parts[i] = int64(q)                     //nolint:gosec // q <= total, so it fits
		rems[i] = r
		assigned += parts[i]
	}
	distribute(parts, rems, total-assigned)
	return parts, nil
}

// normalize validates weights, substitutes equal weights when all are zero,
// and returns the weights with their checked sum.
func normalize(weights []int64) ([]int64, int64, error) {
	var sum int64
	for i, wi := range weights {
		if wi < 0 {
			return nil, 0, fmt.Errorf("%w: index %d is %d", ErrNegativeWeight, i, wi)
		}
		if wi > 0 && sum > (1<<63-1)-wi {
			return nil, 0, ErrWeightOverflow
		}
		sum += wi
	}
	if sum != 0 || len(weights) == 0 {
		return weights, sum, nil
	}
	even := make([]int64, len(weights))
	for i := range even {
		even[i] = 1
	}
	return even, int64(len(even)), nil
}

// distribute gives one extra unit to each of the `leftover` entries with the
// largest remainders, lowest index first on ties.
func distribute(parts []int64, rems []uint64, leftover int64) {
	if leftover <= 0 {
		return
	}
	order := make([]int, len(parts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rems[order[a]] > rems[order[b]] })
	for _, idx := range order[:leftover] {
		parts[idx]++
	}
}
