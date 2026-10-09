package cli_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/postalservice14/itemize-ynab/internal/cli"
)

func TestFormatCents(t *testing.T) {
	tests := map[string]struct {
		cents int64
		want  string
	}{
		"zero":           {0, "$0.00"},
		"one cent":       {1, "$0.01"},
		"ten cents":      {10, "$0.10"},
		"under a dollar": {99, "$0.99"},
		"typical":        {18583, "$185.83"},
		"whole dollars":  {500, "$5.00"},
		"large":          {123456789012, "$1234567890.12"},
		"negative":       {-1299, "-$12.99"},
		"negative cent":  {-1, "-$0.01"},
		"max int64":      {math.MaxInt64, "$92233720368547758.07"},
		"min int64":      {math.MinInt64, "-$92233720368547758.08"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, cli.FormatCents(tc.cents))
		})
	}
}
