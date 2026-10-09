package walmart

import (
	"errors"
	"fmt"
	"math"
	"time"

	wm "github.com/eshaffer321/walmart-client-go/v2"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

// orderDateLayouts are tried in order. The real format of Order.OrderDate and
// OrderSummary.DeliveredDate is unverified, so several plausible layouts are
// accepted; an unparseable value yields the zero time, never a guess.
var orderDateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02",
	"Jan 2, 2006",
	"January 2, 2006",
}

// parseOrderDate returns the date-only UTC wall-clock day of s, or the zero
// time when s is empty or matches no known layout.
func parseOrderDate(s string) time.Time {
	for _, layout := range orderDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return order.DateOnly(t)
		}
	}
	return time.Time{}
}

// convertOrder maps the client order (without ledger data) to the domain order.
func convertOrder(o *wm.Order) (order.Order, error) {
	out := order.Order{ID: o.ID, DisplayID: o.DisplayID, Date: parseOrderDate(o.OrderDate)}

	items, err := convertItems(o.GetItems())
	if err != nil {
		return order.Order{}, err
	}
	out.Items = items

	if out.TaxCents, err = taxCents(o); err != nil {
		return order.Order{}, fmt.Errorf("tax: %w", err)
	}
	if out.TipCents, err = tipCents(o); err != nil {
		return order.Order{}, fmt.Errorf("tip: %w", err)
	}
	if out.FeeCents, err = feeCents(o); err != nil {
		return order.Order{}, fmt.Errorf("fees: %w", err)
	}
	return out, nil
}

func convertItems(in []wm.OrderItem) ([]order.Item, error) {
	items := make([]order.Item, 0, len(in))
	for _, it := range in {
		name := ""
		if it.ProductInfo != nil {
			name = it.ProductInfo.Name
		}
		cents, err := itemLineCents(it)
		if err != nil {
			return nil, fmt.Errorf("item %q: %w", name, err)
		}
		items = append(items, order.Item{
			Name:          name,
			Quantity:      it.Quantity,
			SubtotalCents: cents,
			Refunded:      it.ReturnID != "",
		})
	}
	return items, nil
}

// itemLineCents returns the LINE total in cents. The client's LinePrice is the
// line total and is used when present. Otherwise the unit price is multiplied
// by the quantity in integer arithmetic, which is only exact for a whole
// quantity; a fractional quantity (weighed goods) without a line price is an
// error rather than a guess.
func itemLineCents(it wm.OrderItem) (int64, error) {
	if it.PriceInfo != nil && it.PriceInfo.LinePrice != nil {
		return order.CentsFromDollars(it.PriceInfo.LinePrice.Value)
	}
	if it.PriceInfo == nil || it.PriceInfo.UnitPrice == nil {
		return 0, errors.New("no price")
	}
	if it.Quantity != math.Trunc(it.Quantity) || it.Quantity < 0 {
		return 0, fmt.Errorf("fractional quantity %v without a line price", it.Quantity)
	}
	unit, err := order.CentsFromDollars(it.PriceInfo.UnitPrice.Value)
	if err != nil {
		return 0, err
	}
	return unit * int64(it.Quantity), nil
}

func lineCents(p *wm.PriceLineItem) (int64, error) {
	if p == nil {
		return 0, nil
	}
	return order.CentsFromDollars(p.Value)
}

func moneyCents(m *wm.Money) (int64, error) {
	if m == nil {
		return 0, nil
	}
	return order.CentsFromDollars(m.Value)
}

// groupSum adds a group-level amount over all groups.
func groupSum(o *wm.Order, pick func(*wm.PriceDetails) *wm.Money) (int64, error) {
	var total int64
	for _, g := range o.Groups {
		if g.PriceDetails == nil {
			continue
		}
		c, err := moneyCents(pick(g.PriceDetails))
		if err != nil {
			return 0, err
		}
		total += c
	}
	return total, nil
}

// taxCents prefers the order-level tax and falls back to the sum of the group
// level taxes when the order-level field is absent.
func taxCents(o *wm.Order) (int64, error) {
	if o.PriceDetails != nil && o.PriceDetails.TaxTotal != nil {
		return lineCents(o.PriceDetails.TaxTotal)
	}
	return groupSum(o, func(p *wm.PriceDetails) *wm.Money {
		if p.Tax == nil {
			return nil
		}
		return p.Tax.TaxAmount
	})
}

func tipCents(o *wm.Order) (int64, error) {
	if o.PriceDetails != nil && o.PriceDetails.DriverTip != nil {
		return lineCents(o.PriceDetails.DriverTip)
	}
	return groupSum(o, func(p *wm.PriceDetails) *wm.Money { return p.DriverTip })
}

// feeCents sums the order-level fees (delivery and others); when none are
// present it falls back to the group-level delivery fees.
func feeCents(o *wm.Order) (int64, error) {
	if o.PriceDetails != nil && len(o.PriceDetails.Fees) > 0 {
		var total int64
		for i := range o.PriceDetails.Fees {
			c, err := lineCents(&o.PriceDetails.Fees[i])
			if err != nil {
				return 0, err
			}
			total += c
		}
		return total, nil
	}
	return groupSum(o, func(p *wm.PriceDetails) *wm.Money { return p.DeliveryFee })
}
