package order

import (
	"fmt"
	"math"
)

// maxDollars bounds accepted amounts. Nothing a household order produces comes
// near it, so a larger value means corrupt upstream data.
const maxDollars = 1e9

// CentsFromDollars converts a float dollar amount from a retailer client into
// integer cents. It is the ONLY float-to-cents conversion in the codebase.
//
// It uses math.Round(x*100), which rounds half away from zero. The product is
// computed in binary floating point, so a decimal "tie" such as 1.005 (stored
// as 1.00499999999999989...) rounds DOWN to 100; that is accepted because real
// retailer amounts carry at most two decimals, where x*100 lands within a few
// ulps of an integer and Round recovers it exactly (4.12 -> 412, 0.1+0.2 -> 30).
// NaN, infinities and magnitudes at or above 1e9 dollars return an error.
func CentsFromDollars(dollars float64) (int64, error) {
	if math.IsNaN(dollars) || math.IsInf(dollars, 0) {
		return 0, fmt.Errorf("order: invalid dollar amount %v", dollars)
	}
	if math.Abs(dollars) >= maxDollars {
		return 0, fmt.Errorf("order: dollar amount %v out of range", dollars)
	}
	return int64(math.Round(dollars * 100)), nil
}
