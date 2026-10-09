package sync

import (
	"fmt"
	"math"
)

const milliPerCent = 10

// validateJob is the last guard before YNAB: it runs before any request and
// rejects a job whose splits are not negative, categorized and summing EXACTLY
// to the charge, so such a split can never be sent.
func validateJob(job ChargeJob) error {
	key := job.Charge.Key
	invalid := func(format string, args ...any) error {
		return &InvalidJobError{Key: key, Reason: fmt.Sprintf(format, args...)}
	}
	cents := job.Charge.AmountCents
	switch {
	case key == "":
		return invalid("charge has no key")
	case cents <= 0:
		return invalid("charge amount %d cents is not positive", cents)
	case cents > math.MaxInt64/milliPerCent:
		return invalid("charge amount %d cents does not fit in milliunits", cents)
	case len(job.Splits) == 0:
		return invalid("no splits")
	}
	want := -cents * milliPerCent
	var sum int64
	for i, s := range job.Splits {
		if s.CategoryID == "" {
			return invalid("split %d has no category", i)
		}
		if s.AmountMilli >= 0 {
			return invalid("split %d amount %d is not negative", i, s.AmountMilli)
		}
		// Check BEFORE adding so the addition can never wrap around. The
		// invariant want <= sum <= 0 holds here, so want-sum is in [want, 0]
		// and cannot overflow; an amount below it would push the sum past
		// the charge. This also rejects any single split larger than the
		// charge.
		if s.AmountMilli < want-sum {
			return invalid("splits exceed the charge at split %d: amount %d, only %d left", i, s.AmountMilli, want-sum)
		}
		sum += s.AmountMilli
	}
	if sum != want {
		return invalid("splits sum to %d milliunits, want exactly %d", sum, want)
	}
	return nil
}
