package cli

import "fmt"

// FormatCents renders integer cents as dollars, e.g. 18583 -> "$185.83" and
// -1299 -> "-$12.99". It is the only place cents become text; there is no
// float math, and math.MinInt64 is handled.
func FormatCents(cents int64) string {
	sign := ""
	abs := uint64(cents) //nolint:gosec // reinterpreted as unsigned on purpose; negated below when negative
	if cents < 0 {
		sign = "-"
		abs = -abs // two's complement: exact for MinInt64 too
	}
	return fmt.Sprintf("%s$%d.%02d", sign, abs/100, abs%100)
}
