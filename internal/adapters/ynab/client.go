package ynab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the production YNAB API.
	DefaultBaseURL = "https://api.ynab.com/v1"

	defaultTimeout  = 30 * time.Second
	maxResponseSize = 64 << 20
)

// Client talks to one YNAB plan with a personal access token. It is safe for
// concurrent use. It never retries or sleeps: a 429 surfaces as ErrRateLimited.
type Client struct {
	token   secret
	planID  string
	baseURL string
	http    *http.Client
}

// Option customizes a Client.
type Option func(*Client)

// WithBaseURL overrides the API base URL (used by tests).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// NewClient builds a client for planID (which may be "last-used").
func NewClient(token, planID string, opts ...Option) *Client {
	c := &Client{
		token:   secret(token),
		planID:  planID,
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: defaultTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// String implements fmt.Stringer without exposing the token.
func (c Client) String() string {
	return fmt.Sprintf("ynab.Client{plan:%s token:%s}", c.planID, redacted)
}

// GoString implements fmt.GoStringer without exposing the token.
func (c Client) GoString() string { return c.String() }

// Format makes every fmt verb, including %+v, print the redacted form.
func (c Client) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(c.String())) }

// LogValue implements slog.LogValuer without exposing the token.
func (c Client) LogValue() slog.Value { return slog.StringValue(c.String()) }

// MarshalJSON implements json.Marshaler without exposing the token.
func (c Client) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Detail string `json:"detail"`
	} `json:"error"`
}

func (c *Client) planPath(suffix string) string {
	return "/plans/" + url.PathEscape(c.planID) + suffix
}

// do sends one request and decodes the "data" envelope into out (when non-nil).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("ynab: encode %s %s body: %w", method, path, err)
		}
		reader = bytes.NewReader(b)
	}
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return c.wrap(fmt.Errorf("ynab: build %s %s: %w", method, path, err))
	}
	req.Header.Set("Authorization", "Bearer "+string(c.token))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return c.wrap(fmt.Errorf("ynab: %s %s: %w", method, path, err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return c.wrap(fmt.Errorf("ynab: read %s %s response: %w", method, path, err))
	}
	return c.decode(method, path, resp.StatusCode, raw, out)
}

func (c *Client) decode(method, path string, status int, raw []byte, out any) error {
	var env envelope
	jsonErr := json.Unmarshal(raw, &env)
	if status < 200 || status > 299 {
		apiErr := &APIError{Method: method, Path: path, Status: status}
		if jsonErr == nil && env.Error != nil {
			apiErr.ID = c.token.scrub(env.Error.ID)
			apiErr.Name = c.token.scrub(env.Error.Name)
			apiErr.Detail = c.token.scrub(env.Error.Detail)
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if jsonErr != nil {
		return c.wrap(fmt.Errorf("ynab: decode %s %s response: %w", method, path, jsonErr))
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return c.wrap(fmt.Errorf("ynab: decode %s %s data: %w", method, path, err))
	}
	return nil
}

// wrap strips the token from err's message while keeping the cause matchable
// with errors.Is and errors.As.
func (c *Client) wrap(err error) error {
	msg := err.Error()
	scrubbed := c.token.scrub(msg)
	if scrubbed == msg && !errors.Is(err, context.Canceled) {
		return err
	}
	return &scrubbedError{msg: scrubbed, cause: err}
}

// ListCategories returns every category in the plan, flattened out of its
// group (hidden and deleted included; see EligibleCategories).
func (c *Client) ListCategories(ctx context.Context) ([]Category, error) {
	var data struct {
		Groups []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Hidden     bool   `json:"hidden"`
			Deleted    bool   `json:"deleted"`
			Categories []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Hidden  bool   `json:"hidden"`
				Deleted bool   `json:"deleted"`
			} `json:"categories"`
		} `json:"category_groups"`
	}
	if err := c.do(ctx, http.MethodGet, c.planPath("/categories"), nil, nil, &data); err != nil {
		return nil, err
	}
	var out []Category
	for _, g := range data.Groups {
		for _, cat := range g.Categories {
			out = append(out, Category{
				ID:        cat.ID,
				Name:      cat.Name,
				GroupID:   g.ID,
				GroupName: g.Name,
				Hidden:    cat.Hidden || g.Hidden,
				Deleted:   cat.Deleted || g.Deleted,
			})
		}
	}
	return out, nil
}

// ListAccounts returns every account in the plan.
func (c *Client) ListAccounts(ctx context.Context) ([]Account, error) {
	var data struct {
		Accounts []Account `json:"accounts"`
	}
	if err := c.do(ctx, http.MethodGet, c.planPath("/accounts"), nil, nil, &data); err != nil {
		return nil, err
	}
	return data.Accounts, nil
}

// ListTransactions returns transactions, optionally limited by date or as a
// delta since a previous server_knowledge. Deleted transactions are included
// in delta replies with Deleted set.
func (c *Client) ListTransactions(ctx context.Context, q TransactionsQuery) (TransactionsResult, error) {
	query := url.Values{}
	if !q.SinceDate.IsZero() {
		query.Set("since_date", q.SinceDate.String())
	}
	if q.LastKnowledge > 0 {
		query.Set("last_knowledge_of_server", strconv.FormatInt(q.LastKnowledge, 10))
	}
	var data struct {
		Transactions    []Transaction `json:"transactions"`
		ServerKnowledge int64         `json:"server_knowledge"`
	}
	if err := c.do(ctx, http.MethodGet, c.planPath("/transactions"), query, nil, &data); err != nil {
		return TransactionsResult{}, err
	}
	return TransactionsResult{Transactions: data.Transactions, ServerKnowledge: data.ServerKnowledge}, nil
}

// GetTransaction returns one transaction by ID.
func (c *Client) GetTransaction(ctx context.Context, id string) (Transaction, error) {
	var data struct {
		Transaction Transaction `json:"transaction"`
	}
	path := c.planPath("/transactions/" + url.PathEscape(id))
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &data); err != nil {
		return Transaction{}, err
	}
	return data.Transaction, nil
}

// UpdateTransaction sends PUT /transactions/{id} and returns the saved
// transaction. YNAB does not support changing the subtransactions of a
// transaction that is already a split.
func (c *Client) UpdateTransaction(ctx context.Context, id string, t SaveTransaction) (Transaction, error) {
	var data struct {
		Transaction Transaction `json:"transaction"`
	}
	path := c.planPath("/transactions/" + url.PathEscape(id))
	body := map[string]any{"transaction": t}
	if err := c.do(ctx, http.MethodPut, path, nil, body, &data); err != nil {
		return Transaction{}, err
	}
	return data.Transaction, nil
}

// CreateTransaction sends POST /transactions with one transaction and returns
// it as saved.
func (c *Client) CreateTransaction(ctx context.Context, t SaveTransaction) (Transaction, error) {
	var data struct {
		Transaction  *Transaction  `json:"transaction"`
		Transactions []Transaction `json:"transactions"`
	}
	body := map[string]any{"transaction": t}
	if err := c.do(ctx, http.MethodPost, c.planPath("/transactions"), nil, body, &data); err != nil {
		return Transaction{}, err
	}
	switch {
	case data.Transaction != nil:
		return *data.Transaction, nil
	case len(data.Transactions) > 0:
		return data.Transactions[0], nil
	default:
		return Transaction{}, errors.New("ynab: create transaction: response contained no transaction")
	}
}
