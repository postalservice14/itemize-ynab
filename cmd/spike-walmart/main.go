// Command spike-walmart is a THROWAWAY exploration program, not part of the
// product. It exists so John can run it once against his own Walmart session
// and confirm the response shapes recorded in docs/ARCHITECTURE.md. Delete it
// once the OrderProvider adapter has real-data tests.
//
// It makes live calls to walmart.com, so it is never run by tests or CI.
//
// Usage:
//
//	go run ./cmd/spike-walmart                      # newest order from history
//	go run ./cmd/spike-walmart -order <orderId>     # a specific order
//	go run ./cmd/spike-walmart -curl curl.txt       # first import cookies from a
//	                                                # browser "Copy as cURL" file
//
// Cookies are read from ~/.walmart-api/cookies.json (the client's default).
// That file is NOT a flat name->value map; the client writes it itself when
// given a cURL capture via -curl. Cookie values are never printed. Order IDs
// are masked to their last 4 characters, and customer fields are not printed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	walmart "github.com/eshaffer321/walmart-client-go/v2"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "spike failed:", err)
		os.Exit(1)
	}
}

func run() error {
	orderFlag := flag.String("order", "", "order ID to inspect (default: newest in purchase history)")
	curlFlag := flag.String("curl", "", "optional path to a browser 'Copy as cURL' file to import cookies from")
	flag.Parse()

	// MaxRetries -1 disables the client's own 429 backoff so rate limiting is
	// visible to the spike. LedgerRateLimit is generous because the ledger
	// endpoint is the strictest.
	client, err := walmart.NewWalmartClient(walmart.ClientConfig{
		RateLimit:       2 * time.Second,
		LedgerRateLimit: 15 * time.Second,
		MaxRetries:      -1,
	})
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	if *curlFlag != "" {
		if err := client.InitializeFromCurl(*curlFlag); err != nil {
			return fmt.Errorf("importing cookies from curl file: %w", err)
		}
	}
	fmt.Printf("cookies loaded: %d (values never printed)\n", client.CookieCount())
	if client.CookieCount() == 0 {
		return errors.New("no cookies; re-run with -curl <file> (see package comment)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	summary, err := pickOrder(ctx, client, *orderFlag)
	if err != nil {
		return err
	}
	printSummary(summary)

	order, err := client.GetOrderWithGroup(ctx, summary.OrderID, summary.GroupID, summary.Type == "IN_STORE")
	if err != nil {
		return explain("GetOrder", err)
	}
	printOrder(order)

	ledger, err := client.GetOrderLedger(ctx, summary.OrderID)
	if err != nil {
		return explain("GetOrderLedger", err)
	}
	printLedger(ledger)
	return nil
}

// pickOrder returns the summary for the requested order, or the newest order
// in purchase history when none is requested.
func pickOrder(ctx context.Context, client *walmart.WalmartClient, id string) (walmart.OrderSummary, error) {
	req := walmart.PurchaseHistoryRequest{Limit: 20}
	if id == "" {
		req.Limit = 1
	}
	resp, err := client.GetPurchaseHistory(ctx, req)
	if err != nil {
		return walmart.OrderSummary{}, explain("GetPurchaseHistory", err)
	}
	groups := resp.Data.OrderHistoryV2.OrderGroups
	fmt.Printf("purchase history: %d groups, nextPageCursor set: %t\n",
		len(groups), resp.Data.OrderHistoryV2.PageInfo.NextPageCursor != "")
	if id == "" {
		if len(groups) == 0 {
			return walmart.OrderSummary{}, errors.New("purchase history is empty")
		}
		return groups[0], nil
	}
	for _, g := range groups {
		if g.OrderID == id {
			return g, nil
		}
	}
	// Not in the first page; fall back to a bare summary (group "0").
	return walmart.OrderSummary{OrderID: id, GroupID: "0"}, nil
}

// explain labels an error and flags the typed bot-challenge case.
func explain(call string, err error) error {
	if errors.Is(err, walmart.ErrBotChallenge) {
		return fmt.Errorf("%s: BOT CHALLENGE (HTTP 456); refresh cookies from the browser: %w", call, err)
	}
	return fmt.Errorf("%s: %w", call, err)
}

func mask(id string) string {
	if len(id) <= 4 {
		return "****"
	}
	return "..." + id[len(id)-4:]
}

func printSummary(s walmart.OrderSummary) {
	fmt.Println("\n== OrderSummary ==")
	fmt.Printf("orderId=%s groupId=%s type=%s fulfillment=%s derived=%s items=%d\n",
		mask(s.OrderID), s.GroupID, s.Type, s.FulfillmentType, s.DerivedFulfillmentType, s.ItemCount)
	if s.DeliveredDate != nil {
		fmt.Println("deliveredDate:", *s.DeliveredDate)
	}
	if s.Status != nil {
		fmt.Println("status.statusType:", s.Status.StatusType)
	}
}

func printOrder(o *walmart.Order) {
	fmt.Println("\n== Order ==")
	fmt.Printf("orderDate=%q timezone=%q type=%q groups=%d\n", o.OrderDate, o.Timezone, o.Type, len(o.Groups))
	if pd := o.PriceDetails; pd != nil {
		fmt.Println("order priceDetails:")
		for _, li := range []struct {
			name string
			v    *walmart.PriceLineItem
		}{{"subTotal", pd.SubTotal}, {"taxTotal", pd.TaxTotal}, {"grandTotal", pd.GrandTotal},
			{"driverTip", pd.DriverTip}, {"totalWithTip", pd.TotalWithTip}, {"savings", pd.Savings}} {
			printLine(li.name, li.v)
		}
		for i := range pd.Fees {
			printLine(fmt.Sprintf("fees[%d]", i), &pd.Fees[i])
		}
	}
	for _, pm := range o.PaymentMethods {
		fmt.Printf("order paymentMethod: type=%s cardType=%s desc=%q\n", pm.PaymentType, pm.CardType, pm.Description)
	}
	for gi, g := range o.Groups {
		fmt.Printf("group[%d]: fulfillment=%s itemCount=%d items=%d categories=%d subGroups=%d\n",
			gi, g.FulfillmentType, g.ItemCount, len(g.Items), len(g.Categories), len(g.SubGroups))
		printGroupMoney(g)
		for _, it := range g.Items {
			printItem(it)
		}
	}
	fmt.Println("refunded items (returnId set):", len(o.GetRefundedItems()))
}

func printGroupMoney(g walmart.OrderGroup) {
	if g.PriceDetails != nil {
		pd := g.PriceDetails
		fmt.Println("  group priceDetails: subTotal/tax/savings/grandTotal/driverTip/deliveryFee/totalWithTip")
		for _, m := range []struct {
			name string
			v    *walmart.Money
		}{{"subTotal", pd.SubTotal}, {"savings", pd.Savings}, {"grandTotal", pd.GrandTotal},
			{"driverTip", pd.DriverTip}, {"deliveryFee", pd.DeliveryFee}, {"totalWithTip", pd.TotalWithTip}} {
			if m.v != nil {
				fmt.Printf("    %s: %v (%q)\n", m.name, m.v.Value, m.v.DisplayValue)
			}
		}
		if pd.Tax != nil && pd.Tax.TaxAmount != nil {
			fmt.Printf("    tax: %v (%q)\n", pd.Tax.TaxAmount.Value, pd.Tax.TaxAmount.DisplayValue)
		}
	}
	if g.PaymentDetails != nil {
		for _, pm := range g.PaymentDetails.PaymentMethods {
			amt := "nil"
			if pm.Amount != nil {
				amt = fmt.Sprint(pm.Amount.Value)
			}
			fmt.Printf("  group payment: %q last4=%q amount=%s\n", pm.DisplayName, pm.Last4Digits, amt)
		}
	}
}

func printItem(it walmart.OrderItem) {
	name, line, unit := "?", "nil", "nil"
	if it.ProductInfo != nil {
		name = it.ProductInfo.Name
	}
	if it.PriceInfo != nil {
		if it.PriceInfo.LinePrice != nil {
			line = fmt.Sprint(it.PriceInfo.LinePrice.Value)
		}
		if it.PriceInfo.UnitPrice != nil {
			unit = fmt.Sprint(it.PriceInfo.UnitPrice.Value)
		}
	}
	fmt.Printf("  item: qty=%v name=%q linePrice=%s unitPrice=%s returnId set=%t\n",
		it.Quantity, name, line, unit, it.ReturnID != "")
}

func printLine(name string, li *walmart.PriceLineItem) {
	if li == nil {
		fmt.Printf("  %s: nil\n", name)
		return
	}
	fmt.Printf("  %s: label=%q value=%v display=%q\n", name, li.Label, li.Value, li.DisplayValue)
}

func printLedger(l *walmart.OrderLedger) {
	fmt.Println("\n== OrderLedger ==")
	fmt.Printf("orderId=%s paymentMethods=%d\n", mask(l.OrderID), len(l.PaymentMethods))
	for _, pm := range l.PaymentMethods {
		fmt.Printf("paymentType=%s cardType=%s lastFour=%q total=%v\n",
			pm.PaymentType, pm.CardType, pm.LastFour, pm.TotalCharged)
		for i, amt := range pm.FinalCharges {
			when := "n/a"
			if i < len(pm.ChargedDates) && !pm.ChargedDates[i].IsZero() {
				when = pm.ChargedDates[i].Format(time.RFC3339)
			}
			fmt.Printf("  charge[%d]: %v at %s\n", i, amt, when)
		}
	}
}
