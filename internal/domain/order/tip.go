package order

// TipCharges returns the indexes, in charge order, of the charges that pay
// only the driver tip. A delivery can be charged in pieces with the tip
// charged on its own later.
//
// When any charge carries a ledger label (Kind is not KindUnknown), the
// labels decide: every KindTip charge is a tip charge and nothing else is.
// Otherwise the amounts decide, and one charge is recognized only when the
// order has a tip, exactly one charge equals it, and the other charges add up
// to the order total without the tip (item subtotals plus tax and fees).
func TipCharges(o Order) []int {
	if anyLabeled(o.Charges) {
		var tips []int
		for i, c := range o.Charges {
			if c.Kind == KindTip {
				tips = append(tips, i)
			}
		}
		return tips
	}
	if i, ok := tipByAmount(o); ok {
		return []int{i}
	}
	return nil
}

func anyLabeled(charges []Charge) bool {
	for _, c := range charges {
		if c.Kind != KindUnknown {
			return true
		}
	}
	return false
}

func tipByAmount(o Order) (int, bool) {
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
