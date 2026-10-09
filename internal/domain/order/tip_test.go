package order

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// tipOrder is the live delivery: $104.50 of items, $9.13 tax, a $5.00 fee and
// a $7.51 driver tip charged on its own after six charges totalling $118.63.
func tipOrder() Order {
	return Order{
		Items:    []Item{{Name: "a", SubtotalCents: 10000}, {Name: "b", SubtotalCents: 450}},
		TaxCents: 913, FeeCents: 500, TipCents: 751,
		Charges: []Charge{
			{AmountCents: 751}, {AmountCents: 570}, {AmountCents: 579}, {AmountCents: 839},
			{AmountCents: 372}, {AmountCents: 9021}, {AmountCents: 482},
		},
	}
}

func TestTipCharge_findsTheSeparatelyChargedTip(t *testing.T) {
	i, ok := TipCharge(tipOrder())

	assert.True(t, ok)
	assert.Equal(t, 0, i)
}

func TestTipCharge_none(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Order)
	}{
		{"no tip", func(o *Order) { o.TipCents = 0 }},
		{"no charge equals the tip", func(o *Order) { o.Charges[0].AmountCents = 750 }},
		{
			"two charges equal the tip",
			func(o *Order) {
				o.Charges = append(o.Charges, Charge{AmountCents: 751})
				o.Items[0].SubtotalCents += 751
			},
		},
		{"tip folded into another charge", func(o *Order) { o.Charges[5].AmountCents += 751 }},
		{"the rest is not the order total", func(o *Order) { o.FeeCents = 0 }},
		{"only charge", func(o *Order) { o.Charges = []Charge{{AmountCents: 751}} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := tipOrder()
			tc.mutate(&o)
			_, ok := TipCharge(o)
			assert.False(t, ok)
		})
	}
}
