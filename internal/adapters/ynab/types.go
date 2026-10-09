// Package ynab is a small client for the YNAB API (https://api.ynab.com/v1),
// built on net/http and encoding/json only.
//
// Amounts are int64 milliunits (USD cents x 10); outflows are negative.
package ynab

import (
	"encoding/json"
	"fmt"
	"time"
)

const dateLayout = "2006-01-02"

// Date is a calendar date, serialized as YYYY-MM-DD. The zero value means
// "unset" and is omitted from requests.
type Date time.Time

// NewDate builds a Date at midnight UTC.
func NewDate(year int, month time.Month, day int) Date {
	return Date(time.Date(year, month, day, 0, 0, 0, 0, time.UTC))
}

// Time returns the date as a time.Time at midnight UTC.
func (d Date) Time() time.Time { return time.Time(d) }

// IsZero reports whether the date is unset.
func (d Date) IsZero() bool { return time.Time(d).IsZero() }

// String formats the date as YYYY-MM-DD, or "" when unset.
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return time.Time(d).Format(dateLayout)
}

// MarshalJSON implements json.Marshaler.
func (d Date) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("ynab: decode date: %w", err)
	}
	if s == "" {
		*d = Date{}
		return nil
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return fmt.Errorf("ynab: parse date %q: %w", s, err)
	}
	*d = Date(t)
	return nil
}

// Category is a plan category flattened out of its group. Hidden and Deleted
// are true when either the category or its group has the flag.
type Category struct {
	ID        string
	Name      string
	GroupID   string
	GroupName string
	Hidden    bool
	Deleted   bool
}

// Account is a plan account.
type Account struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	OnBudget bool   `json:"on_budget"`
	Closed   bool   `json:"closed"`
	Deleted  bool   `json:"deleted"`
}

// Transaction is a YNAB transaction as returned by the API.
type Transaction struct {
	ID                      string           `json:"id"`
	Date                    Date             `json:"date"`
	Amount                  int64            `json:"amount"`
	Memo                    string           `json:"memo"`
	Cleared                 string           `json:"cleared"`
	Approved                bool             `json:"approved"`
	FlagColor               string           `json:"flag_color"`
	AccountID               string           `json:"account_id"`
	PayeeID                 *string          `json:"payee_id"`
	PayeeName               string           `json:"payee_name"`
	CategoryID              *string          `json:"category_id"`
	CategoryName            string           `json:"category_name"`
	TransferAccountID       *string          `json:"transfer_account_id"`
	ImportID                string           `json:"import_id"`
	ImportPayeeName         string           `json:"import_payee_name"`
	ImportPayeeNameOriginal string           `json:"import_payee_name_original"`
	Deleted                 bool             `json:"deleted"`
	SubTransactions         []SubTransaction `json:"subtransactions"`
}

// SubTransaction is one part of a split transaction.
type SubTransaction struct {
	ID                string  `json:"id"`
	TransactionID     string  `json:"transaction_id"`
	Amount            int64   `json:"amount"`
	Memo              string  `json:"memo"`
	CategoryID        *string `json:"category_id"`
	TransferAccountID *string `json:"transfer_account_id"`
	Deleted           bool    `json:"deleted"`
}

// IsSplit reports whether the transaction has any live subtransactions.
func (t Transaction) IsSplit() bool {
	for _, s := range t.SubTransactions {
		if !s.Deleted {
			return true
		}
	}
	return false
}

// IsTransfer reports whether the transaction moves money between accounts.
func (t Transaction) IsTransfer() bool {
	return t.TransferAccountID != nil && *t.TransferAccountID != ""
}

// TransactionsQuery holds the optional filters for ListTransactions.
type TransactionsQuery struct {
	// SinceDate limits results to transactions on or after this date. YNAB
	// defaults to one year ago when it is unset.
	SinceDate Date
	// LastKnowledge requests a delta: only changes since that server_knowledge.
	// Zero means a full request.
	LastKnowledge int64
}

// TransactionsResult is the reply of ListTransactions.
type TransactionsResult struct {
	Transactions    []Transaction
	ServerKnowledge int64
}

// SaveTransaction is the body of a create (POST) or update (PUT). Only the
// fields that are set are sent, so an update can change just the memo.
type SaveTransaction struct {
	AccountID  string
	Date       Date
	Amount     *int64
	PayeeName  string
	CategoryID *string
	// ClearCategory sends category_id as an explicit null (uncategorized).
	// It is implied when SubTransactions are set.
	ClearCategory bool
	Memo          *string
	Cleared       string
	Approved      *bool
	FlagColor     *string
	// SubTransactions turns the transaction into a split. When any are set,
	// category_id is sent as an explicit null, as YNAB requires for a split.
	SubTransactions []SaveSubTransaction
}

// SaveSubTransaction is one part of a split being saved. The amounts of all
// parts must sum exactly to the parent amount.
type SaveSubTransaction struct {
	Amount     int64
	CategoryID string
	Memo       string
}

type wireSave struct {
	AccountID       string           `json:"account_id,omitempty"`
	Date            string           `json:"date,omitempty"`
	Amount          *int64           `json:"amount,omitempty"`
	PayeeName       string           `json:"payee_name,omitempty"`
	CategoryID      json.RawMessage  `json:"category_id,omitempty"`
	Memo            *string          `json:"memo,omitempty"`
	Cleared         string           `json:"cleared,omitempty"`
	Approved        *bool            `json:"approved,omitempty"`
	FlagColor       *string          `json:"flag_color,omitempty"`
	SubTransactions []wireSaveSubTxn `json:"subtransactions,omitempty"`
}

type wireSaveSubTxn struct {
	Amount     int64  `json:"amount"`
	CategoryID string `json:"category_id,omitempty"`
	Memo       string `json:"memo,omitempty"`
}

// MarshalJSON implements json.Marshaler.
func (s SaveTransaction) MarshalJSON() ([]byte, error) {
	w := wireSave{
		AccountID: s.AccountID,
		Date:      s.Date.String(),
		Amount:    s.Amount,
		PayeeName: s.PayeeName,
		Memo:      s.Memo,
		Cleared:   s.Cleared,
		Approved:  s.Approved,
		FlagColor: s.FlagColor,
	}
	switch {
	case len(s.SubTransactions) > 0:
		w.CategoryID = json.RawMessage("null")
		for _, p := range s.SubTransactions {
			w.SubTransactions = append(w.SubTransactions, wireSaveSubTxn(p))
		}
	case s.ClearCategory && s.CategoryID == nil:
		w.CategoryID = json.RawMessage("null")
	case s.CategoryID != nil:
		b, err := json.Marshal(*s.CategoryID)
		if err != nil {
			return nil, fmt.Errorf("ynab: encode category_id: %w", err)
		}
		w.CategoryID = b
	}
	return json.Marshal(w)
}
