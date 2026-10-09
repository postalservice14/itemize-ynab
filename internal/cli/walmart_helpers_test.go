package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab/ynabtest"
	"github.com/postalservice14/itemize-ynab/internal/cli"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

const (
	wmToken  = "wm-ynab-token-SECRET" //nolint:gosec // recognizable fake, not a credential
	cardA    = "1111"
	acctA    = "acct-a"
	catGroc  = "cat-groc"
	catHome  = "cat-home"
	planPath = "/plans/plan-1"
)

var wmNow = time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)

func day(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

// fakeProvider serves scripted orders; it never touches a network.
type fakeProvider struct {
	refs    []order.OrderRef
	orders  map[string]order.Order
	errs    map[string]error
	listErr error
	calls   int
}

func (p *fakeProvider) Orders(context.Context, time.Time) ([]order.OrderRef, error) {
	p.calls++
	return p.refs, p.listErr
}

func (p *fakeProvider) Order(_ context.Context, ref order.OrderRef) (order.Order, error) {
	p.calls++
	if err := p.errs[ref.ID]; err != nil {
		return order.Order{}, err
	}
	return p.orders[ref.ID], nil
}

func (p *fakeProvider) add(orders ...order.Order) {
	for _, o := range orders {
		p.refs = append(p.refs, order.OrderRef{ID: o.ID})
		p.orders[o.ID] = o
	}
}

var itemsBlock = regexp.MustCompile(`(?s)<items>\n(.*)\n</items>`)

// fakeChat answers the categorizer prompt from a name -> category table.
type fakeChat struct {
	byItem map[string]string
	calls  int
}

func (c *fakeChat) Chat(_ context.Context, req categorizer.ChatRequest) (string, error) {
	c.calls++
	m := itemsBlock.FindStringSubmatch(req.User)
	if m == nil {
		return "", fmt.Errorf("fakeChat: no items in prompt")
	}
	var items []struct {
		Index int    `json:"index"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal([]byte(m[1]), &items); err != nil {
		return "", err
	}
	type entry struct {
		Index    int    `json:"index"`
		Category string `json:"category"`
	}
	out := make([]entry, 0, len(items))
	for _, it := range items {
		out = append(out, entry{it.Index, c.byItem[it.Name]})
	}
	b, err := json.Marshal(map[string]any{"items": out})
	return string(b), err
}

func item(name string, cents int64) order.Item {
	return order.Item{Name: name, Quantity: 1, SubtotalCents: cents}
}

func charge(orderID string, cents int64, d time.Time) order.Charge {
	return order.Charge{
		Key: order.ChargeKey("walmart", orderID, cents, 1), OrderID: orderID, AmountCents: cents,
		Date: d, DateSource: order.ChargeDate, LastFour: cardA, PaymentType: "CREDITCARD",
	}
}

// orderA is one $5.00 single-category charge; tA is its YNAB transaction.
func orderA() (order.Order, ynab.Transaction) {
	o := order.Order{
		ID: "oA", DisplayID: "A-100", Date: day(10, 5),
		Items:   []order.Item{item("Milk", 300), item("Bread", 200)},
		Charges: []order.Charge{charge("oA", 500, day(10, 5))},
	}
	return o, wmTxn("tA", -5000, day(10, 5))
}

// orderB is one $100.00 charge split 60/40 over two categories.
func orderB() (order.Order, ynab.Transaction) {
	o := order.Order{
		ID: "oB", DisplayID: "B-200", Date: day(10, 6),
		Items:   []order.Item{item("Towels", 6000), item("Apples", 4000)},
		Charges: []order.Charge{charge("oB", 10000, day(10, 6))},
	}
	return o, wmTxn("tB", -100000, day(10, 6))
}

func wmTxn(id string, milli int64, d time.Time) ynab.Transaction {
	return ynab.Transaction{
		ID: id, AccountID: acctA, Amount: milli, Date: ynab.NewDate(d.Date()),
		PayeeName: "Walmart", ImportPayeeNameOriginal: "WAL-MART #1234", Cleared: "cleared",
	}
}

func splitAs(orig ynab.Transaction, parts ...[2]any) ynab.Transaction {
	out := orig
	out.CategoryID = nil
	for i, p := range parts {
		cat := p[0].(string)
		out.SubTransactions = append(out.SubTransactions, ynab.SubTransaction{
			ID: fmt.Sprintf("%s-sub-%d", orig.ID, i), TransactionID: orig.ID, Amount: p[1].(int64), CategoryID: &cat,
		})
	}
	return out
}

func savedTxn(t ynab.Transaction) ynabtest.Response { return ynabtest.OK(ynabtest.TransactionData(t)) }

// wmHarness wires cli.Run to a fake YNAB server, a fake provider, a fake LLM
// and a real SQLite file in a temp dir.
type wmHarness struct {
	t      *testing.T
	srv    *ynabtest.Server
	dir    string
	cfg    string
	prov   *fakeProvider
	chat   *fakeChat
	getenv map[string]string
	sleeps int
	// factory counters
	provFactory, chatFactory int
	// useDefault* leave the matching factory nil so the real one runs.
	useDefaultProvider, useDefaultChat bool
	// onSleep runs inside the injected Sleep (for example to cancel a context).
	onSleep func()
	stdin   string
}

func newWMHarness(t *testing.T) *wmHarness {
	t.Helper()
	t.Setenv("YNAB_TOKEN", wmToken)
	h := &wmHarness{
		t: t, srv: ynabtest.New(t), dir: t.TempDir(),
		prov: &fakeProvider{orders: map[string]order.Order{}, errs: map[string]error{}},
		chat: &fakeChat{byItem: map[string]string{
			"Milk": "Groceries", "Bread": "Groceries", "Apples": "Groceries", "Towels": "Household",
		}},
		getenv: map[string]string{},
	}
	h.writeConfig("")
	h.srv.On(http.MethodGet, planPath+"/categories", ynabtest.OK(ynabtest.CategoriesData(1,
		ynabtest.Group{ID: "g-food", Name: "Food", Categories: []ynabtest.Cat{{ID: catGroc, Name: "Groceries"}}},
		ynabtest.Group{ID: "g-home", Name: "Home", Categories: []ynabtest.Cat{{ID: catHome, Name: "Household"}}},
	)))
	h.srv.On(http.MethodGet, planPath+"/accounts", ynabtest.OK(map[string]any{"accounts": []ynab.Account{{ID: acctA}}}))
	return h
}

// writeConfig writes config.yaml with the database and cookie file in the temp
// dir; extra is appended verbatim.
func (h *wmHarness) writeConfig(extra string) {
	h.t.Helper()
	h.cfg = filepath.Join(h.dir, "config.yaml")
	body := fmt.Sprintf(`ynab:
  token: "${YNAB_TOKEN}"
  plan_id: plan-1
  accounts:
    "%s": %s
database:
  path: %q
walmart:
  cookie_file: %q
%s`, cardA, acctA, filepath.Join(h.dir, "iy.db"), filepath.Join(h.dir, "cookies.json"), extra)
	require.NoError(h.t, os.WriteFile(h.cfg, []byte(body), 0o600))
}

// script serves orders A and B with their YNAB transactions and accepting PUTs.
func (h *wmHarness) script() {
	a, tA := orderA()
	b, tB := orderB()
	h.prov.add(a, b)
	h.srv.On(http.MethodGet, planPath+"/transactions", ynabtest.OK(ynabtest.TransactionsData(10, tA, tB)))
	h.srv.On(http.MethodPut, planPath+"/transactions/tA", savedTxn(tA))
	h.srv.On(http.MethodPut, planPath+"/transactions/tB",
		savedTxn(splitAs(tB, [2]any{catHome, int64(-60000)}, [2]any{catGroc, int64(-40000)})))
}

func (h *wmHarness) env(stdout, stderr *bytes.Buffer) cli.Env {
	env := cli.Env{
		Stdout:        stdout,
		Stderr:        stderr,
		Stdin:         strings.NewReader(h.stdin),
		ClientOptions: []ynab.Option{ynab.WithBaseURL(h.srv.URL())},
		Getenv:        func(k string) string { return h.getenv[k] },
		Deps: cli.Deps{
			Now: func() time.Time { return wmNow },
			Sleep: func(ctx context.Context, _ time.Duration) error {
				h.sleeps++
				if h.onSleep != nil {
					h.onSleep()
				}
				return ctx.Err()
			},
		},
	}
	if !h.useDefaultProvider {
		env.Deps.NewProvider = func(*config.Config, *slog.Logger) (order.OrderProvider, error) {
			h.provFactory++
			return h.prov, nil
		}
	}
	if !h.useDefaultChat {
		env.Deps.NewChat = func(func(string) string) (categorizer.ChatClient, error) {
			h.chatFactory++
			return h.chat, nil
		}
	}
	return env
}

// run executes `itemize-ynab -config <cfg> args...`.
func (h *wmHarness) run(args ...string) result {
	h.t.Helper()
	return h.runCtx(context.Background(), args...)
}

func (h *wmHarness) runCtx(ctx context.Context, args ...string) result {
	h.t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Run(ctx, append([]string{"-config", h.cfg}, args...), h.env(&out, &errOut))
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// untouched asserts that nothing reached YNAB, Walmart or the LLM.
func (h *wmHarness) untouched() {
	h.t.Helper()
	require.Empty(h.t, h.srv.Requests(), "no YNAB request may be made")
	require.Zero(h.t, h.prov.calls, "no Walmart call may be made")
	require.Zero(h.t, h.chat.calls, "no LLM call may be made")
}
