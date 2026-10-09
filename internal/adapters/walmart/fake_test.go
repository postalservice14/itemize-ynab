package walmart

import (
	"context"

	wm "github.com/eshaffer321/walmart-client-go/v2"
)

// fakeClient is a hand-rolled fake of the narrow Client interface.
type fakeClient struct {
	history     func(ctx context.Context, req wm.PurchaseHistoryRequest) (*wm.PurchaseHistoryResponse, error)
	order       func(ctx context.Context, orderID, groupID string, inStore bool) (*wm.Order, error)
	ledger      func(ctx context.Context, orderID string) (*wm.OrderLedger, error)
	historyReqs []wm.PurchaseHistoryRequest
	orderCalls  []string
}

func (f *fakeClient) GetPurchaseHistory(ctx context.Context, req wm.PurchaseHistoryRequest) (*wm.PurchaseHistoryResponse, error) {
	f.historyReqs = append(f.historyReqs, req)
	return f.history(ctx, req)
}

func (f *fakeClient) GetOrderWithGroup(ctx context.Context, orderID, groupID string, inStore bool) (*wm.Order, error) {
	f.orderCalls = append(f.orderCalls, orderID+"|"+groupID)
	return f.order(ctx, orderID, groupID, inStore)
}

func (f *fakeClient) GetOrderLedger(ctx context.Context, orderID string) (*wm.OrderLedger, error) {
	return f.ledger(ctx, orderID)
}

func historyPage(next string, summaries ...wm.OrderSummary) *wm.PurchaseHistoryResponse {
	var r wm.PurchaseHistoryResponse
	r.Data.OrderHistoryV2.PageInfo.NextPageCursor = next
	r.Data.OrderHistoryV2.OrderGroups = summaries
	return &r
}
