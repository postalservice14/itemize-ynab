package sync

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateJob_reasons pins which guard rejects each job. The writer table
// test only checks that a job is rejected, and for a non-positive charge the
// later split guards would reject it too, so only the reason shows that the
// amount guard itself fired.
func TestValidateJob_reasons(t *testing.T) {
	tests := map[string]struct {
		mutate func(*ChargeJob)
		want   string
	}{
		"zero charge":     {func(j *ChargeJob) { j.Charge.AmountCents = 0 }, "charge amount 0 cents is not positive"},
		"negative charge": {func(j *ChargeJob) { j.Charge.AmountCents = -5000 }, "charge amount -5000 cents is not positive"},
		"zero charge without splits": {func(j *ChargeJob) { j.Charge.AmountCents, j.Splits = 0, nil },
			"charge amount 0 cents is not positive"},
		"no key":    {func(j *ChargeJob) { j.Charge.Key = "" }, "charge has no key"},
		"no splits": {func(j *ChargeJob) { j.Splits = nil }, "no splits"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			job := multiJob("k1", 5000, date(10, 4), acctA)
			tc.mutate(&job)

			err := validateJob(job)

			var invalid *InvalidJobError
			require.True(t, errors.As(err, &invalid), "want *InvalidJobError, got %v", err)
			assert.Equal(t, tc.want, invalid.Reason)
		})
	}
	require.NoError(t, validateJob(multiJob("k1", 5000, date(10, 4), acctA)), "the base job is valid")
}
