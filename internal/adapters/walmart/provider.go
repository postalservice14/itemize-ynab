// Package walmart adapts walmart-client-go to the retailer-agnostic
// order.OrderProvider port.
//
// The adapter never sleeps or paces requests itself; the 2s spacing between
// Walmart requests is the orchestrator's job (the client's own rate limiter is
// the only wait in here). Cookies and headers never appear in errors or logs.
package walmart

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	wm "github.com/eshaffer321/walmart-client-go/v2"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

// ProviderName is the key prefix for Walmart charge keys.
const ProviderName = "walmart"

const (
	historyPageSize = 20
	// maxHistoryPages bounds pagination in case the cursor never ends.
	maxHistoryPages = 25
	inStoreType     = "IN_STORE"
)

// Provider implements order.OrderProvider over a Client.
type Provider struct {
	client Client
	log    *slog.Logger
}

var _ order.OrderProvider = (*Provider)(nil)

// Option tunes a Provider.
type Option func(*Provider)

// WithLogger sets the logger; the default discards output.
func WithLogger(l *slog.Logger) Option {
	return func(p *Provider) {
		if l != nil {
			p.log = l
		}
	}
}

// New returns a Provider over client.
func New(client Client, opts ...Option) *Provider {
	p := &Provider{client: client, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Orders lists orders from purchase history, following pagination. since is
// sent as the history MinTimestamp (the client's filtering is unverified), and
// summaries whose delivered date parses and falls before since are dropped as
// a second line of defense. Summaries without an order ID are ignored. History
// lists a multi-shipment order once per group, but the detail and ledger cover
// the whole order, so each order ID is returned once, with its first group.
func (p *Provider) Orders(ctx context.Context, since time.Time) ([]order.OrderRef, error) {
	var (
		refs   []order.OrderRef
		seen   = map[string]bool{}
		cursor string
		minDay = order.DateOnly(since)
	)
	for page := 0; page < maxHistoryPages; page++ {
		if err := ctx.Err(); err != nil {
			return refs, fmt.Errorf("walmart: listing orders: %w", err)
		}
		req := wm.PurchaseHistoryRequest{Cursor: cursor, Limit: historyPageSize}
		if !since.IsZero() {
			ts := since.Unix()
			req.MinTimestamp = &ts
		}
		resp, err := p.client.GetPurchaseHistory(ctx, req)
		if err != nil {
			return refs, wrapError("-", "list purchase history", err)
		}
		hist := resp.Data.OrderHistoryV2
		for _, s := range hist.OrderGroups {
			if s.OrderID == "" || seen[s.OrderID] || summaryTooOld(s, minDay) {
				continue
			}
			seen[s.OrderID] = true
			refs = append(refs, order.OrderRef{ID: s.OrderID, Token: encodeToken(s.GroupID, s.Type == inStoreType)})
		}
		cursor = hist.PageInfo.NextPageCursor
		if cursor == "" {
			break
		}
	}
	p.log.Debug("listed walmart orders", slog.Int("orders", len(refs)))
	return refs, nil
}

func summaryTooOld(s wm.OrderSummary, minDay time.Time) bool {
	if minDay.IsZero() || s.DeliveredDate == nil {
		return false
	}
	d := parseOrderDate(*s.DeliveredDate)
	return !d.IsZero() && d.Before(minDay)
}

// Order fetches the order detail and its ledger and derives the charges. A
// blocked session returns an error matching order.ErrBlocked; any other
// failure is wrapped with the order ID.
func (p *Provider) Order(ctx context.Context, ref order.OrderRef) (order.Order, error) {
	groupID, inStore := decodeToken(ref.Token)
	wo, err := p.client.GetOrderWithGroup(ctx, ref.ID, groupID, inStore)
	if err != nil {
		return order.Order{}, wrapError(ref.ID, "get order", err)
	}
	if wo == nil {
		return order.Order{}, fmt.Errorf("walmart: order %s: empty order response", ref.ID)
	}
	out, err := convertOrder(wo)
	if err != nil {
		return order.Order{}, fmt.Errorf("walmart: order %s: %w", ref.ID, err)
	}
	if out.ID == "" {
		out.ID = ref.ID
	}

	ledger, err := p.client.GetOrderLedger(ctx, ref.ID)
	if err != nil {
		return order.Order{}, wrapError(ref.ID, "get ledger", err)
	}
	out.Charges, out.Skipped, err = deriveCharges(out.ID, out.Date, ledger)
	if err != nil {
		return order.Order{}, fmt.Errorf("walmart: order %s: %w", ref.ID, err)
	}
	p.log.Debug("fetched walmart order", slog.String("order_id", out.ID),
		slog.Int("items", len(out.Items)), slog.Int("charges", len(out.Charges)), slog.Int("skipped", len(out.Skipped)))
	return out, nil
}

// encodeToken packs the group ID and in-store hint into the opaque ref token.
func encodeToken(groupID string, inStore bool) string {
	flag := "D"
	if inStore {
		flag = "I"
	}
	return groupID + "|" + flag
}

// decodeToken reverses encodeToken; an empty or malformed token yields the
// client's default group "0" and a delivery lookup.
func decodeToken(token string) (groupID string, inStore bool) {
	i := strings.LastIndex(token, "|")
	if i < 0 {
		return "0", false
	}
	groupID = token[:i]
	if groupID == "" {
		groupID = "0"
	}
	return groupID, token[i+1:] == "I"
}
