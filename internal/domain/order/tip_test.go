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

func TestTipCharges_findsTheSeparatelyChargedTip(t *testing.T) {
	assert.Equal(t, []int{0}, TipCharges(tipOrder()))
}

func TestTipCharges_none(t *testing.T) {
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
			assert.Empty(t, TipCharges(o))
		})
	}
}

// labeled marks every charge of o with the order-charge kind except the ones
// at tips, which are marked as tips.
func labeled(o Order, tips ...int) Order {
	for i := range o.Charges {
		o.Charges[i].Kind = KindOrder
	}
	for _, i := range tips {
		o.Charges[i].Kind = KindTip
	}
	return o
}

func TestTipCharges_labelsWinOverTheAmountCheck(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Order)
	}{
		{"labels agree with the amounts", func(*Order) {}},
		{"order exposes no tip", func(o *Order) { o.TipCents = 0 }},
		{"tip changed after the order", func(o *Order) { o.Charges[0].AmountCents = 900 }},
		{"rest is not the order total", func(o *Order) { o.FeeCents = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := labeled(tipOrder(), 0)
			tc.mutate(&o)
			assert.Equal(t, []int{0}, TipCharges(o))
		})
	}
}

func TestTipCharges_everyLabeledTipIsATip(t *testing.T) {
	o := tipOrder()
	o.Charges = append(o.Charges, Charge{AmountCents: 200})

	assert.Equal(t, []int{0, 7}, TipCharges(labeled(o, 0, 7)))
}

func TestTipCharges_labeledLedgerWithoutATipHasNone(t *testing.T) {
	// The amounts alone would pick charge 0; the labels say it pays the order.
	assert.Empty(t, TipCharges(labeled(tipOrder())))
}

func TestTipCharges_onlyOneChargeLabeledStillTrustsLabels(t *testing.T) {
	o := tipOrder()
	o.Charges[3].Kind = KindOrder

	assert.Empty(t, TipCharges(o), "a labeled ledger is never second-guessed by amounts")
}
