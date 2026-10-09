// Package order holds the retailer-agnostic order and charge types shared by
// order providers (adapters) and their consumers. It is pure: standard library
// only, no clock, no I/O.
package order

import (
	"fmt"
	"time"
)

// DateSource records where a Charge.Date came from.
type DateSource int

// Date sources.
const (
	// ChargeDate: the date the retailer's ledger recorded for the charge.
	ChargeDate DateSource = iota + 1
	// OrderDateFallback: the ledger had no usable date, so the order date was
	// used. If the order date was unavailable too, Charge.Date is the zero time
	// and the consumer must decide what to do; no date is ever invented.
	OrderDateFallback
)

// SkipReason says why a ledger entry did not become a Charge.
type SkipReason int

// Skip reasons.
const (
	// GiftCard: paid with a gift card.
	GiftCard SkipReason = iota + 1
	// NonCardPayment: any other non-card payment type (e.g. store credit).
	NonCardPayment
	// Refund: a negative ledger amount (refund or adjustment).
	Refund
	// ZeroAmount: a ledger entry of exactly zero.
	ZeroAmount
)

// String returns a short label for logs and reports.
func (r SkipReason) String() string {
	switch r {
	case GiftCard:
		return "gift card"
	case NonCardPayment:
		return "non-card payment"
	case Refund:
		return "refund"
	case ZeroAmount:
		return "zero amount"
	default:
		return fmt.Sprintf("unknown(%d)", int(r))
	}
}

// Order is one retailer order with the data needed to split its charges.
type Order struct {
	ID        string
	DisplayID string
	// Date is the order date, date-only UTC; zero when the retailer data had
	// no parseable date.
	Date  time.Time
	Items []Item
	// TaxCents, TipCents and FeeCents are order totals as the retailer exposes
	// them; 0 when the component is absent. FeeCents sums delivery and other fees.
	TaxCents, TipCents, FeeCents int64
	// Charges are the card charges, in ledger order. Every AmountCents > 0.
	Charges []Charge
	// Skipped are ledger entries that are intentionally not Charges.
	Skipped []SkippedCharge
}

// Item is one order line.
type Item struct {
	Name     string
	Quantity float64
	// SubtotalCents is the LINE total (unit price x quantity as priced by the
	// retailer), the amount the splitter weights by. It is not a unit price.
	SubtotalCents int64
	// Refunded marks a line the retailer flagged as returned. It is still
	// listed because the original charge covered it; consumers decide.
	Refunded bool
}

// Charge is one card charge on an order.
type Charge struct {
	// Key is the stable idempotency key, see ChargeKey.
	Key     string
	OrderID string
	// AmountCents is always > 0.
	AmountCents int64
	// Date is date-only UTC midnight of the wall-clock calendar day.
	Date       time.Time
	DateSource DateSource
	LastFour   string
	// PaymentType and CardType are the retailer's raw labels.
	PaymentType string
	CardType    string
}

// SkippedCharge is a ledger entry that is not a card charge to match.
type SkippedCharge struct {
	// AmountCents keeps the ledger sign (negative for refunds).
	AmountCents int64
	Reason      SkipReason
	PaymentType string
	LastFour    string
	Note        string
}

// ChargeKey builds the stable key for a charge:
// "<provider>:<orderID>:<amountCents>:<occurrence>", where occurrence is the
// 1-based count of card charges of that exact amount within the order, in
// ledger order.
func ChargeKey(provider, orderID string, amountCents int64, occurrence int) string {
	return fmt.Sprintf("%s:%s:%d:%d", provider, orderID, amountCents, occurrence)
}

// ResolveAccount maps a card's last four digits to a YNAB account ID. ok is
// false for an empty last four, an unknown one, or one mapped to an empty ID.
func ResolveAccount(lastFour string, byLastFour map[string]string) (accountID string, ok bool) {
	if lastFour == "" {
		return "", false
	}
	id := byLastFour[lastFour]
	if id == "" {
		return "", false
	}
	return id, true
}

// DateOnly returns midnight UTC of t's own wall-clock year/month/day, ignoring
// its location's offset. The zero time stays zero.
func DateOnly(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
