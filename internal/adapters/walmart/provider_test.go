package walmart

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	wm "github.com/eshaffer321/walmart-client-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

var _ order.OrderProvider = (*Provider)(nil)

func price(v float64) *wm.Price { return &wm.Price{Value: v} }

func fullOrder() *wm.Order {
	return &wm.Order{
		ID:        "W1",
		DisplayID: "2000-1",
		OrderDate: "2026-03-01T10:15:00-05:00",
		PriceDetails: &wm.OrderPriceDetails{
			TaxTotal:  &wm.PriceLineItem{Value: 2.34},
			DriverTip: &wm.PriceLineItem{Value: 5},
			Fees:      []wm.PriceLineItem{{Value: 7.95}, {Value: 1.05}},
		},
		Groups: []wm.OrderGroup{{Items: []wm.OrderItem{
			{Quantity: 2, ProductInfo: &wm.ProductInfo{Name: "Milk"}, PriceInfo: &wm.ItemPrice{LinePrice: price(7.98), UnitPrice: price(3.99)}},
			{Quantity: 1.5, ProductInfo: &wm.ProductInfo{Name: "Apples"}, PriceInfo: &wm.ItemPrice{LinePrice: price(4.50)}},
			{Quantity: 3, ReturnID: "R1", ProductInfo: &wm.ProductInfo{Name: "Soap"}, PriceInfo: &wm.ItemPrice{UnitPrice: price(1.10)}},
		}}},
	}
}

func okLedger() *wm.OrderLedger {
	return &wm.OrderLedger{PaymentMethods: []wm.PaymentMethodCharges{
		card("0953", []float64{25.00}, []time.Time{d1}),
	}}
}

func newTestProvider(f *fakeClient) *Provider { return New(f) }

func TestProvider_Order_happyPath(t *testing.T) {
	f := &fakeClient{
		order:  func(context.Context, string, string, bool) (*wm.Order, error) { return fullOrder(), nil },
		ledger: func(context.Context, string) (*wm.OrderLedger, error) { return okLedger(), nil },
	}
	got, err := newTestProvider(f).Order(context.Background(), order.OrderRef{ID: "W1", Token: encodeToken("G9", true)})
	require.NoError(t, err)

	assert.Equal(t, []string{"W1|G9"}, f.orderCalls)
	assert.Equal(t, "W1", got.ID)
	assert.Equal(t, "2000-1", got.DisplayID)
	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), got.Date, "wall-clock day of the order date")
	assert.Equal(t, int64(234), got.TaxCents)
	assert.Equal(t, int64(500), got.TipCents)
	assert.Equal(t, int64(900), got.FeeCents)

	require.Len(t, got.Items, 3)
	assert.Equal(t, order.Item{Name: "Milk", Quantity: 2, SubtotalCents: 798}, got.Items[0])
	assert.Equal(t, int64(450), got.Items[1].SubtotalCents, "line price wins over unit price")
	assert.Equal(t, int64(330), got.Items[2].SubtotalCents, "unit price x whole quantity when no line price")
	assert.True(t, got.Items[2].Refunded)

	require.Len(t, got.Charges, 1)
	assert.Equal(t, "walmart:W1:2500:1", got.Charges[0].Key)
	assert.Equal(t, order.ChargeDate, got.Charges[0].DateSource)
}

func TestProvider_Order_groupLevelFallbacks(t *testing.T) {
	o := &wm.Order{ID: "W2", Groups: []wm.OrderGroup{
		{PriceDetails: &wm.PriceDetails{Tax: &wm.TaxInfo{TaxAmount: price(1.10)}, DriverTip: price(2), DeliveryFee: price(3)}},
		{PriceDetails: &wm.PriceDetails{Tax: &wm.TaxInfo{TaxAmount: price(0.25)}}},
	}}
	f := &fakeClient{
		order:  func(context.Context, string, string, bool) (*wm.Order, error) { return o, nil },
		ledger: func(context.Context, string) (*wm.OrderLedger, error) { return &wm.OrderLedger{}, nil },
	}
	got, err := newTestProvider(f).Order(context.Background(), order.OrderRef{ID: "W2"})
	require.NoError(t, err)
	assert.Equal(t, int64(135), got.TaxCents)
	assert.Equal(t, int64(200), got.TipCents)
	assert.Equal(t, int64(300), got.FeeCents)
	assert.True(t, got.Date.IsZero())
	assert.Empty(t, got.Charges)
}

func TestProvider_Order_dateFormats(t *testing.T) {
	for in, want := range map[string]time.Time{
		"2026-03-01T23:30:00-05:00": time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		"2026-03-01":                time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		"Mar 1, 2026":               time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		"March 1, 2026":             time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		"garbage":                   {},
		"":                          {},
	} {
		assert.Equal(t, want, parseOrderDate(in), in)
	}
}

func TestProvider_Order_errors(t *testing.T) {
	blockedErr := fmt.Errorf("%w: HTTP 456", wm.ErrBotChallenge)
	tests := []struct {
		name      string
		orderErr  error
		ledgerErr error
		blocked   bool
	}{
		{"order bot challenge", blockedErr, nil, true},
		{"order stale", errors.New("access denied - cookies expired"), nil, true},
		{"ledger rate limited", nil, errors.New("after 3 retries: rate limited (attempt 4/4)"), true},
		{"ledger stale", nil, errors.New("access denied (cookies might be stale)"), true},
		{"order other", errors.New("no order data in response"), nil, false},
		{"ledger other", nil, errors.New("unexpected status code: 500"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeClient{
				order: func(context.Context, string, string, bool) (*wm.Order, error) {
					if tt.orderErr != nil {
						return nil, tt.orderErr
					}
					return fullOrder(), nil
				},
				ledger: func(context.Context, string) (*wm.OrderLedger, error) {
					if tt.ledgerErr != nil {
						return nil, tt.ledgerErr
					}
					return okLedger(), nil
				},
			}
			_, err := newTestProvider(f).Order(context.Background(), order.OrderRef{ID: "W1"})
			require.Error(t, err)
			assert.Equal(t, tt.blocked, errors.Is(err, order.ErrBlocked))
			assert.Contains(t, err.Error(), "W1")
		})
	}
}

func TestProvider_Order_nilOrderAndBadMoney(t *testing.T) {
	f := &fakeClient{
		order:  func(context.Context, string, string, bool) (*wm.Order, error) { return nil, nil },
		ledger: func(context.Context, string) (*wm.OrderLedger, error) { return okLedger(), nil },
	}
	_, err := newTestProvider(f).Order(context.Background(), order.OrderRef{ID: "W1"})
	assert.Error(t, err)

	bad := fullOrder()
	bad.PriceDetails.TaxTotal.Value = math.Inf(1)
	f.order = func(context.Context, string, string, bool) (*wm.Order, error) { return bad, nil }
	_, err = newTestProvider(f).Order(context.Background(), order.OrderRef{ID: "W1"})
	assert.Error(t, err)

	noPrice := fullOrder()
	noPrice.Groups[0].Items = []wm.OrderItem{{Quantity: 1.5, ProductInfo: &wm.ProductInfo{Name: "X"}, PriceInfo: &wm.ItemPrice{UnitPrice: price(2)}}}
	f.order = func(context.Context, string, string, bool) (*wm.Order, error) { return noPrice, nil }
	_, err = newTestProvider(f).Order(context.Background(), order.OrderRef{ID: "W1"})
	assert.Error(t, err, "fractional quantity without a line price cannot be priced exactly")
}

func TestProvider_Orders_paginatesAndFilters(t *testing.T) {
	since := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	old := "2026-01-01"
	recent := "2026-03-02"
	pages := map[string]*wm.PurchaseHistoryResponse{
		"": historyPage("c2",
			wm.OrderSummary{OrderID: "A", GroupID: "GA", Type: "IN_STORE", DeliveredDate: &recent},
			wm.OrderSummary{OrderID: "", GroupID: "GX"},
			wm.OrderSummary{OrderID: "OLD", GroupID: "GO", DeliveredDate: &old}),
		"c2": historyPage("", wm.OrderSummary{OrderID: "B", GroupID: "GB", Type: "GLASS"}),
	}
	f := &fakeClient{history: func(_ context.Context, r wm.PurchaseHistoryRequest) (*wm.PurchaseHistoryResponse, error) {
		return pages[r.Cursor], nil
	}}
	refs, err := newTestProvider(f).Orders(context.Background(), since)
	require.NoError(t, err)

	require.Len(t, refs, 2)
	assert.Equal(t, "A", refs[0].ID)
	assert.Equal(t, encodeToken("GA", true), refs[0].Token)
	assert.Equal(t, encodeToken("GB", false), refs[1].Token)

	require.Len(t, f.historyReqs, 2)
	require.NotNil(t, f.historyReqs[0].MinTimestamp)
	assert.Equal(t, since.Unix(), *f.historyReqs[0].MinTimestamp)
}

func TestProvider_Orders_listsEachOrderOnce(t *testing.T) {
	// History lists a multi-shipment order once per group, across pages too;
	// the order detail and ledger cover the whole order, so one ref is enough.
	pages := map[string]*wm.PurchaseHistoryResponse{
		"": historyPage("c2",
			wm.OrderSummary{OrderID: "A", GroupID: "GA1"},
			wm.OrderSummary{OrderID: "B", GroupID: "GB"},
			wm.OrderSummary{OrderID: "A", GroupID: "GA2"}),
		"c2": historyPage("", wm.OrderSummary{OrderID: "A", GroupID: "GA3"}),
	}
	f := &fakeClient{history: func(_ context.Context, r wm.PurchaseHistoryRequest) (*wm.PurchaseHistoryResponse, error) {
		return pages[r.Cursor], nil
	}}
	refs, err := newTestProvider(f).Orders(context.Background(), time.Time{})
	require.NoError(t, err)

	require.Len(t, refs, 2)
	assert.Equal(t, "A", refs[0].ID)
	assert.Equal(t, encodeToken("GA1", false), refs[0].Token, "the first group wins")
	assert.Equal(t, "B", refs[1].ID)
}

func TestProvider_Orders_errorsAndLimits(t *testing.T) {
	f := &fakeClient{history: func(context.Context, wm.PurchaseHistoryRequest) (*wm.PurchaseHistoryResponse, error) {
		return nil, fmt.Errorf("failed on page 1: %w", errors.New("access denied - cookies expired"))
	}}
	_, err := newTestProvider(f).Orders(context.Background(), time.Time{})
	assert.ErrorIs(t, err, order.ErrBlocked)
	assert.Nil(t, f.historyReqs[0].MinTimestamp, "zero since sends no timestamp")

	// An endless cursor is bounded.
	calls := 0
	f = &fakeClient{history: func(context.Context, wm.PurchaseHistoryRequest) (*wm.PurchaseHistoryResponse, error) {
		calls++
		return historyPage("again", wm.OrderSummary{OrderID: fmt.Sprint(calls)}), nil
	}}
	refs, err := newTestProvider(f).Orders(context.Background(), time.Time{})
	require.NoError(t, err)
	assert.Len(t, refs, maxHistoryPages)

	// Cancelled context stops paging.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = newTestProvider(f).Orders(ctx, time.Time{})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestToken(t *testing.T) {
	g, in := decodeToken(encodeToken("G1", true))
	assert.Equal(t, "G1", g)
	assert.True(t, in)
	g, in = decodeToken(encodeToken("G1", false))
	assert.Equal(t, "G1", g)
	assert.False(t, in)
	g, in = decodeToken("")
	assert.Equal(t, "0", g, "empty token falls back to the client's default group")
	assert.False(t, in)
}

func TestProvider_Order_itemWithoutAnyPrice(t *testing.T) {
	o := fullOrder()
	o.Groups[0].Items = []wm.OrderItem{{Quantity: 1, ProductInfo: &wm.ProductInfo{Name: "Mystery"}}}
	f := &fakeClient{
		order:  func(context.Context, string, string, bool) (*wm.Order, error) { return o, nil },
		ledger: func(context.Context, string) (*wm.OrderLedger, error) { return okLedger(), nil },
	}
	_, err := newTestProvider(f).Order(context.Background(), order.OrderRef{ID: "W1"})
	assert.Error(t, err)
}
