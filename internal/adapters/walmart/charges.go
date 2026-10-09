package walmart

import (
	"fmt"
	"strings"
	"time"

	wm "github.com/eshaffer321/walmart-client-go/v2"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

const (
	paymentCreditCard = "CREDITCARD"
	paymentGiftCard   = "GIFTCARD"
)

// deriveCharges turns a ledger into card Charges and Skipped entries.
//
// Rules: only CREDITCARD payments become charges; negative amounts (refunds,
// adjustments) and zero amounts are skipped regardless of payment type; gift
// cards and other payment types are skipped. Skipped entries are never counted
// in the key occurrence, so a gift-card line cannot renumber card charges.
// Occurrence is the 1-based count of card charges of the same cent amount in
// ledger order across all payment methods.
//
// A charge date is the ledger date (date-only UTC of its wall-clock day), or
// the order date when the ledger date is missing, zero or the date slice is
// shorter than the charge slice. If the order date is also zero the charge
// date is zero and its source is OrderDateFallback; no date is invented.
func deriveCharges(orderID string, orderDate time.Time, ledger *wm.OrderLedger) ([]order.Charge, []order.SkippedCharge, error) {
	if ledger == nil {
		return nil, nil, nil
	}
	var (
		charges []order.Charge
		skipped []order.SkippedCharge
		seen    = map[int64]int{}
	)
	for _, pm := range ledger.PaymentMethods {
		ptype := strings.ToUpper(strings.TrimSpace(pm.PaymentType))
		for i, dollars := range pm.FinalCharges {
			cents, err := order.CentsFromDollars(dollars)
			if err != nil {
				return nil, nil, fmt.Errorf("ledger charge %d: %w", i, err)
			}
			if reason, skip := skipReason(ptype, cents); skip {
				skipped = append(skipped, order.SkippedCharge{
					AmountCents: cents,
					Reason:      reason,
					PaymentType: pm.PaymentType,
					LastFour:    pm.LastFour,
					Note:        pm.CardType,
				})
				continue
			}
			seen[cents]++
			date, source := chargeDate(pm.ChargedDates, i, orderDate)
			charges = append(charges, order.Charge{
				Key:         order.ChargeKey(ProviderName, orderID, cents, seen[cents]),
				OrderID:     orderID,
				AmountCents: cents,
				Date:        date,
				DateSource:  source,
				LastFour:    pm.LastFour,
				PaymentType: pm.PaymentType,
				CardType:    pm.CardType,
			})
		}
	}
	return charges, skipped, nil
}

func skipReason(paymentType string, cents int64) (order.SkipReason, bool) {
	switch {
	case cents < 0:
		return order.Refund, true
	case cents == 0:
		return order.ZeroAmount, true
	case paymentType == paymentCreditCard:
		return 0, false
	case paymentType == paymentGiftCard:
		return order.GiftCard, true
	default:
		return order.NonCardPayment, true
	}
}

func chargeDate(dates []time.Time, i int, orderDate time.Time) (time.Time, order.DateSource) {
	if i < len(dates) && !dates[i].IsZero() {
		return order.DateOnly(dates[i]), order.ChargeDate
	}
	return orderDate, order.OrderDateFallback
}
