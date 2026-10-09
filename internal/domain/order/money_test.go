package order

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCentsFromDollars(t *testing.T) {
	// A runtime sum: a constant 0.1 + 0.2 is folded exactly to 0.3, so it
	// would never exercise binary rounding (0.30000000000000004).
	a, b := 0.1, 0.2
	tests := []struct {
		name string
		in   float64
		want int64
	}{
		{"typical", 4.12, 412},
		{"larger", 178.96, 17896},
		{"binary sum 0.1+0.2", a + b, 30},
		{"seven cents", 0.07, 7},
		{"1.005 is stored below the tie so rounds down", 1.005, 100},
		{"zero", 0, 0},
		{"negative refund", -15.00, -1500},
		{"negative rounds away from zero", -0.075, -8},
		{"19.999 rounds up", 19.999, 2000},
		{"large", 99999.99, 9999999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CentsFromDollars(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCentsFromDollars_rejects(t *testing.T) {
	for name, in := range map[string]float64{
		"NaN":      math.NaN(),
		"+Inf":     math.Inf(1),
		"-Inf":     math.Inf(-1),
		"absurd":   1e12,
		"absurdNg": -1e12,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := CentsFromDollars(in)
			assert.Error(t, err)
		})
	}
}
