package sync

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/domain/splitter"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

const (
	cardA          = "1111"
	catPets        = "cat-pets"
	categoriesPath = planBase + "/categories"
	accountsPath   = planBase + "/accounts"
	txnListPath    = planBase + "/transactions"
)

// fakeProvider serves scripted orders and records every call in events.
type fakeProvider struct {
	refs    []order.OrderRef
	orders  map[string]order.Order
	errs    map[string]error
	listErr error
	since   []time.Time
	fetched []string
	events  *[]string
}

func (p *fakeProvider) Orders(_ context.Context, since time.Time) ([]order.OrderRef, error) {
	p.since = append(p.since, since)
	*p.events = append(*p.events, "list")
	return p.refs, p.listErr
}

func (p *fakeProvider) Order(_ context.Context, ref order.OrderRef) (order.Order, error) {
	p.fetched = append(p.fetched, ref.ID)
	*p.events = append(*p.events, "fetch:"+ref.ID)
	if err := p.errs[ref.ID]; err != nil {
		return order.Order{}, err
	}
	return p.orders[ref.ID], nil
}

func (p *fakeProvider) add(orders ...order.Order) {
	for _, o := range orders {
		p.refs = append(p.refs, order.OrderRef{ID: o.ID, Token: "tok-" + o.ID})
		p.orders[o.ID] = o
	}
}

func (p *fakeProvider) calls() int { return len(p.since) + len(p.fetched) }

// fakeCategorizer maps item names to categories and records every call. It
// reports the model's category only (no overrides applied), so the
// orchestrator's own override step is what the tests see, and it rejects any
// answer that is not among the allowed names it was offered.
type fakeCategorizer struct {
	byItem  map[string]string
	errFor  map[string]error
	calls   [][]string
	allowed [][]string
}

func (c *fakeCategorizer) Categorize(_ context.Context, items []order.Item, allowed []string) ([]categorizer.Assignment, error) {
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name)
	}
	c.calls = append(c.calls, names)
	c.allowed = append(c.allowed, allowed)
	for _, it := range items {
		if err := c.errFor[it.Name]; err != nil {
			return nil, err
		}
	}
	out := make([]categorizer.Assignment, 0, len(items))
	for i, it := range items {
		cat := c.byItem[it.Name]
		if !slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, cat) }) {
			// Like the real categorizer: an answer outside the offered names
			// is rejected, never assigned.
			return nil, &categorizer.InvalidResponseError{Problems: []string{
				fmt.Sprintf("unknown category %q, which is not in the allowed list", cat)}}
		}
		out = append(out, categorizer.Assignment{ItemIndex: i, Name: it.Name, Category: cat, ModelCategory: cat})
	}
	return out, nil
}

type orchHarness struct {
	*harness
	prov     *fakeProvider
	cat      *fakeCategorizer
	cfg      config.Config
	events   []string
	sleeps   []time.Duration
	sleepErr error
}

func newOrchHarness(t *testing.T) *orchHarness {
	t.Helper()
	h := &orchHarness{
		harness: newHarness(t),
		cat: &fakeCategorizer{byItem: map[string]string{
			"Milk": "Groceries", "Bread": "groceries", "Apples": "Groceries",
			"Towels": "Household", "Soap": "Household",
		}},
		cfg: config.Config{YNAB: config.YNAB{
			Token: testToken, PlanID: plan, FlagColor: flagColor, SplitInPlace: "auto",
			Accounts:    map[string]string{cardA: acctA},
			MatchWindow: config.MatchWindow{DaysBefore: 2, DaysAfter: 10},
		}},
	}
	h.prov = &fakeProvider{orders: map[string]order.Order{}, errs: map[string]error{}, events: &h.events}
	h.srv.On(http.MethodGet, categoriesPath, ynabtest.OK(ynabtest.CategoriesData(1,
		ynabtest.Group{ID: "g-food", Name: "Food", Categories: []ynabtest.Cat{
			{ID: catGroc, Name: "Groceries"}, {ID: "cat-old", Name: "Old Snacks", Hidden: true},
		}},
		ynabtest.Group{ID: "g-home", Name: "Home", Categories: []ynabtest.Cat{
			{ID: catHome, Name: "Household"}, {ID: catPets, Name: "Pets"},
		}},
		ynabtest.Group{ID: "g-int", Name: "Internal Master Category", Categories: []ynabtest.Cat{
			{ID: "cat-inflow", Name: "Inflow: Ready to Assign"},
		}},
	)))
	h.srv.On(http.MethodGet, accountsPath, ynabtest.OK(map[string]any{"accounts": []ynab.Account{{ID: acctA}}}))
	return h
}

func (h *orchHarness) withTxns(txns ...ynab.Transaction) {
	body := make([]any, 0, len(txns))
	for _, t := range txns {
		body = append(body, t)
	}
	h.srv.On(http.MethodGet, txnListPath, ynabtest.OK(ynabtest.TransactionsData(10, body...)))
}

func (h *orchHarness) sleep(ctx context.Context, d time.Duration) error {
	h.sleeps = append(h.sleeps, d)
	h.events = append(h.events, "sleep:"+d.String())
	if h.sleepErr != nil {
		return h.sleepErr
	}
	return ctx.Err()
}

func (h *orchHarness) orchestrator(t *testing.T) *Orchestrator {
	t.Helper()
	o, err := NewOrchestrator(Deps{
		Provider:     h.prov,
		Categorizer:  h.cat,
		YNAB:         h.api,
		Writes:       h.api,
		Transactions: NewTransactionSource(h.api, h.store, quiet),
		Store:        h.store,
		Config:       h.cfg,
		Now:          func() time.Time { return now },
		Sleep:        h.sleep,
		Logger:       slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	require.NoError(t, err)
	return o
}

func (h *orchHarness) run(t *testing.T, opts Options) (Summary, error) {
	t.Helper()
	return h.orchestrator(t).Run(context.Background(), opts)
}

func (h *orchHarness) txnListCalls() int { return len(h.srv.Calls(http.MethodGet, txnListPath)) }

// resetCounters forgets recorded requests, categorizer calls and events.
func (h *orchHarness) resetCounters() {
	h.srv.Reset()
	h.cat.calls, h.cat.allowed = nil, nil
	h.events, h.sleeps = nil, nil
	h.prov.since, h.prov.fetched = nil, nil
}

func item(name string, cents int64) order.Item {
	return order.Item{Name: name, Quantity: 1, SubtotalCents: cents}
}

func charge(orderID string, cents int64, occ int, d time.Time) order.Charge {
	return order.Charge{
		Key: order.ChargeKey("walmart", orderID, cents, occ), OrderID: orderID, AmountCents: cents,
		Date: d, DateSource: order.ChargeDate, LastFour: cardA, PaymentType: "CREDITCARD",
	}
}

// orderA is one single-category charge of $5.00 matching tA.
func orderA() (order.Order, ynab.Transaction) {
	o := order.Order{
		ID: "oA", DisplayID: "A-100", Date: date(10, 5),
		Items:   []order.Item{item("Milk", 300), item("Bread", 200)},
		Charges: []order.Charge{charge("oA", 500, 1, date(10, 5))},
	}
	return o, wm("tA", acctA, -5000, date(10, 5))
}

// orderB is a 60/40 two-category order paid by two charges whose sum ($105.00)
// is not the item total ($100.00): splits must come from each CHARGE.
func orderB() (order.Order, []ynab.Transaction) {
	o := order.Order{
		ID: "oB", DisplayID: "B-200", Date: date(10, 6), TaxCents: 500,
		Items: []order.Item{item("Towels", 6000), item("Apples", 4000)},
		Charges: []order.Charge{
			charge("oB", 7350, 1, date(10, 6)),
			charge("oB", 3150, 1, date(10, 7)),
		},
	}
	return o, []ynab.Transaction{
		wm("tB1", acctA, -73500, date(10, 6)),
		wm("tB2", acctA, -31500, date(10, 7)),
	}
}

var (
	splitsB1 = []splitter.Split{sp(catHome, -44100, "Towels"), sp(catGroc, -29400, "Apples")}
	splitsB2 = []splitter.Split{sp(catHome, -18900, "Towels"), sp(catGroc, -12600, "Apples")}
)

// happy scripts orders A and B, their transactions and accepting PUTs.
func (h *orchHarness) happy() {
	a, tA := orderA()
	b, tB := orderB()
	h.prov.add(a, b)
	h.withTxns(tA, tB[0], tB[1])
	h.srv.On(http.MethodPut, txnPath("tA"), okTxn(tA))
	h.srv.On(http.MethodPut, txnPath("tB1"), okTxn(savedAs(tB[0], "", splitsB1)))
	h.srv.On(http.MethodPut, txnPath("tB2"), okTxn(savedAs(tB[1], "", splitsB2)))
}

func statuses(rows []Row) []Status {
	out := make([]Status, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Status)
	}
	return out
}

// subAmounts returns the (category, amount) pairs of a sent split.
func subAmounts(t *testing.T, r ynabtest.Request) [][2]any {
	t.Helper()
	var out [][2]any
	for _, s := range subs(t, sent(t, r)) {
		out = append(out, [2]any{s["category_id"], s["amount"]})
	}
	return out
}
