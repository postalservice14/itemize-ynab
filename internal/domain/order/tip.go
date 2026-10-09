package order

// TipCharge returns the index of the charge that pays only the driver tip.
// A delivery can be charged in pieces with the tip charged on its own later;
// that charge is recognized only when the order has a tip, exactly one charge
// equals it, and the other charges add up to the order total without the tip
// (item subtotals plus tax and fees). Anything else is not a tip charge.
func TipCharge(o Order) (int, bool) {
	if o.TipCents <= 0 {
		return 0, false
	}
	idx, matches := 0, 0
	var rest int64
	for i, c := range o.Charges {
		if c.AmountCents == o.TipCents {
			idx = i
			matches++
		}
		rest += c.AmountCents
	}
	if matches != 1 {
		return 0, false
	}
	rest -= o.TipCents
	want := o.TaxCents + o.FeeCents
	for _, it := range o.Items {
		want += it.SubtotalCents
	}
	return idx, rest == want
}
