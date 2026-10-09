package ynabtest

import "net/http"

// OK wraps data in YNAB's {"data": ...} envelope with a 200 status.
func OK(data any) Response {
	return Response{Status: http.StatusOK, Body: map[string]any{"data": data}}
}

// APIError replies with YNAB's {"error": {id, name, detail}} body.
func APIError(status int, id, name, detail string) Response {
	return Response{Status: status, Body: map[string]any{
		"error": map[string]string{"id": id, "name": name, "detail": detail},
	}}
}

// RateLimited replies 429 the way YNAB does when the hourly quota is spent.
func RateLimited() Response {
	return APIError(http.StatusTooManyRequests, "429", "too_many_requests",
		"Too many requests")
}

// Cat describes one category in CategoryGroups.
type Cat struct {
	ID      string
	Name    string
	Hidden  bool
	Deleted bool
}

// Group describes one category group in CategoryGroups.
type Group struct {
	ID         string
	Name       string
	Hidden     bool
	Deleted    bool
	Categories []Cat
}

// CategoriesData builds the data payload of GET /plans/{id}/categories.
func CategoriesData(serverKnowledge int64, groups ...Group) map[string]any {
	wire := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		cats := make([]map[string]any, 0, len(g.Categories))
		for _, c := range g.Categories {
			cats = append(cats, map[string]any{
				"id":                  c.ID,
				"category_group_id":   g.ID,
				"category_group_name": g.Name,
				"name":                c.Name,
				"hidden":              c.Hidden,
				"deleted":             c.Deleted,
			})
		}
		wire = append(wire, map[string]any{
			"id": g.ID, "name": g.Name, "hidden": g.Hidden, "deleted": g.Deleted,
			"categories": cats,
		})
	}
	return map[string]any{"category_groups": wire, "server_knowledge": serverKnowledge}
}

// TransactionsData builds the data payload of GET /plans/{id}/transactions.
// Each transaction is any JSON-encodable value.
func TransactionsData(serverKnowledge int64, txns ...any) map[string]any {
	if txns == nil {
		txns = []any{}
	}
	return map[string]any{"transactions": txns, "server_knowledge": serverKnowledge}
}

// TransactionData builds the data payload of a single-transaction reply
// (GET, PUT and POST on one transaction).
func TransactionData(txn any) map[string]any {
	return map[string]any{"transaction": txn, "server_knowledge": 1}
}
