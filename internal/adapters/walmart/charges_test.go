package walmart

import (
	"math"
	"testing"
	"time"

	wm "github.com/eshaffer321/walmart-client-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

var (
	orderDate = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	d1        = time.Date(2026, 3, 2, 14, 5, 0, 0, time.UTC)
	d2        = time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
)

func card(last string, amounts []float64, dates []time.Time) wm.PaymentMethodCharges {
	return wm.PaymentMethodCharges{PaymentType: "CREDITCARD", CardType: "VISA", LastFour: last, FinalCharges: amounts, ChargedDates: dates}
}

func TestDeriveCharges_occurrenceNumbering(t *testing.T) {
	ledger := &wm.OrderLedger{PaymentMethods: []wm.PaymentMethodCharges{
		card("0953", []float64{45.00, 12.50}, []time.Time{d1, d1}),
		{PaymentType: "GIFTCARD", CardType: "WMGC", FinalCharges: []float64{45.00}, ChargedDates: []time.Time{d1}},
		card("0953", []float64{45.00}, []time.Time{d2}),
	}}

	charges, skipped, err := deriveCharges("W1", orderDate, ledger)
	require.NoError(t, err)

	require.Len(t, charges, 3)
	assert.Equal(t, "walmart:W1:4500:1", charges[0].Key)
	assert.Equal(t, "walmart:W1:1250:1", charges[1].Key)
	assert.Equal(t, "walmart:W1:4500:2", charges[2].Key, "gift card of the same amount must not renumber")
	assert.Equal(t, int64(4500), charges[2].AmountCents)
	assert.Equal(t, "0953", charges[2].LastFour)
	assert.Equal(t, "CREDITCARD", charges[2].PaymentType)
	assert.Equal(t, "VISA", charges[2].CardType)
	assert.Equal(t, "W1", charges[2].OrderID)

	require.Len(t, skipped, 1)
	assert.Equal(t, order.GiftCard, skipped[0].Reason)
	assert.Equal(t, int64(4500), skipped[0].AmountCents)
}

func TestDeriveCharges_giftCardOnlyAndEmpty(t *testing.T) {
	gift := &wm.OrderLedger{PaymentMethods: []wm.PaymentMethodCharges{
		{PaymentType: "GIFTCARD", FinalCharges: []float64{20}, ChargedDates: []time.Time{d1}},
	}}
	charges, skipped, err := deriveCharges("W1", orderDate, gift)
	require.NoError(t, err)
	assert.Empty(t, charges)
	assert.Len(t, skipped, 1)

	for name, l := range map[string]*wm.OrderLedger{
		"nil ledger":    nil,
		"no methods":    {},
		"empty charges": {PaymentMethods: []wm.PaymentMethodCharges{card("0953", nil, nil)}},
	} {
		t.Run(name, func(t *testing.T) {
			charges, skipped, err := deriveCharges("W1", orderDate, l)
			require.NoError(t, err)
			assert.Empty(t, charges)
			assert.Empty(t, skipped)
		})
	}
}

func TestDeriveCharges_nonCardAndZeroAndNegative(t *testing.T) {
	ledger := &wm.OrderLedger{PaymentMethods: []wm.PaymentMethodCharges{
		{PaymentType: "WALMART_CASH", FinalCharges: []float64{3.10}, ChargedDates: []time.Time{d1}},
		card("0953", []float64{-15.00, 0, 8.00}, []time.Time{d1, d1, d1}),
		{PaymentType: "GIFTCARD", FinalCharges: []float64{-2.00}, ChargedDates: []time.Time{d1}},
	}}
	charges, skipped, err := deriveCharges("W1", orderDate, ledger)
	require.NoError(t, err)

	require.Len(t, charges, 1)
	assert.Equal(t, "walmart:W1:800:1", charges[0].Key)
	for _, c := range charges {
		assert.Positive(t, c.AmountCents)
	}

	require.Len(t, skipped, 4)
	assert.Equal(t, order.NonCardPayment, skipped[0].Reason)
	assert.Equal(t, order.Refund, skipped[1].Reason)
	assert.Equal(t, int64(-1500), skipped[1].AmountCents)
	assert.Equal(t, order.ZeroAmount, skipped[2].Reason)
	assert.Equal(t, order.Refund, skipped[3].Reason, "negative gift-card entry is a refund, not a gift-card charge")
}

func TestDeriveCharges_dates(t *testing.T) {
	edt := time.FixedZone("EDT", -4*3600)
	late := time.Date(2026, 3, 5, 23, 30, 0, 0, edt)
	ledger := &wm.OrderLedger{PaymentMethods: []wm.PaymentMethodCharges{
		card("0953", []float64{1, 2, 3}, []time.Time{late, {}, d2}),
	}}
	charges, _, err := deriveCharges("W1", orderDate, ledger)
	require.NoError(t, err)

	assert.Equal(t, time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), charges[0].Date, "wall-clock day, not UTC instant")
	assert.Equal(t, order.ChargeDate, charges[0].DateSource)

	assert.Equal(t, orderDate, charges[1].Date, "zero charged date falls back to order date")
	assert.Equal(t, order.OrderDateFallback, charges[1].DateSource)

	assert.Equal(t, order.ChargeDate, charges[2].DateSource)
}

func TestDeriveCharges_noDateAtAll(t *testing.T) {
	ledger := &wm.OrderLedger{PaymentMethods: []wm.PaymentMethodCharges{card("0953", []float64{5}, []time.Time{{}})}}
	charges, _, err := deriveCharges("W1", time.Time{}, ledger)
	require.NoError(t, err)
	assert.True(t, charges[0].Date.IsZero(), "no date is invented")
	assert.Equal(t, order.OrderDateFallback, charges[0].DateSource)
}

func TestDeriveCharges_mismatchedLengths(t *testing.T) {
	ledger := &wm.OrderLedger{PaymentMethods: []wm.PaymentMethodCharges{
		card("0953", []float64{1, 2, 3}, []time.Time{d1}),
		card("0953", []float64{4}, nil),
	}}
	charges, _, err := deriveCharges("W1", orderDate, ledger)
	require.NoError(t, err)
	require.Len(t, charges, 4)
	assert.Equal(t, order.ChargeDate, charges[0].DateSource)
	assert.Equal(t, order.OrderDateFallback, charges[1].DateSource)
	assert.Equal(t, order.OrderDateFallback, charges[2].DateSource)
	assert.Equal(t, order.OrderDateFallback, charges[3].DateSource)

	// More dates than charges: extras are ignored.
	extra := &wm.OrderLedger{PaymentMethods: []wm.PaymentMethodCharges{card("0953", []float64{1}, []time.Time{d1, d2})}}
	charges, _, err = deriveCharges("W1", orderDate, extra)
	require.NoError(t, err)
	assert.Len(t, charges, 1)
}

func TestDeriveCharges_invalidAmount(t *testing.T) {
	nan := math.NaN()
	ledger := &wm.OrderLedger{PaymentMethods: []wm.PaymentMethodCharges{card("0953", []float64{nan}, nil)}}
	_, _, err := deriveCharges("W1", orderDate, ledger)
	assert.Error(t, err)
}

func TestDeriveCharges_paymentTypeCaseInsensitive(t *testing.T) {
	ledger := &wm.OrderLedger{PaymentMethods: []wm.PaymentMethodCharges{
		{PaymentType: " creditcard ", LastFour: "1", FinalCharges: []float64{1}},
	}}
	charges, _, err := deriveCharges("W1", orderDate, ledger)
	require.NoError(t, err)
	assert.Len(t, charges, 1)
}
