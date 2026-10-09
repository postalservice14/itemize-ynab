package sync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/matcher"
)

func TestToMatcherTxn(t *testing.T) {
	transfer, empty := "acct-savings", ""
	base := ynab.Transaction{
		ID: "t1", AccountID: acctA, Amount: -12340, Date: ynab.NewDate(2026, time.October, 5),
		Memo: "Costco run", PayeeName: "Walmart",
	}
	with := func(f func(*ynab.Transaction)) ynab.Transaction {
		t := base
		f(&t)
		return t
	}
	want := matcher.Txn{
		ID: "t1", AccountID: acctA, PayeeName: "Walmart", Memo: "Costco run",
		AmountMilli: -12340, Date: time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC),
	}
	cases := []struct {
		name string
		in   ynab.Transaction
		edit func(*matcher.Txn)
	}{
		{"plain", base, func(*matcher.Txn) {}},
		{"original import payee preferred",
			with(func(t *ynab.Transaction) { t.ImportPayeeName, t.ImportPayeeNameOriginal = "Walmart", "WAL-MART #12" }),
			func(m *matcher.Txn) { m.ImportPayeeName = "WAL-MART #12" }},
		{"cleaned import payee as fallback",
			with(func(t *ynab.Transaction) { t.ImportPayeeName = "Walmart" }),
			func(m *matcher.Txn) { m.ImportPayeeName = "Walmart" }},
		{"deleted", with(func(t *ynab.Transaction) { t.Deleted = true }), func(m *matcher.Txn) { m.Deleted = true }},
		{"transfer", with(func(t *ynab.Transaction) { t.TransferAccountID = &transfer }),
			func(m *matcher.Txn) { m.IsTransfer = true }},
		{"empty transfer id is not a transfer", with(func(t *ynab.Transaction) { t.TransferAccountID = &empty }),
			func(*matcher.Txn) {}},
		{"split", with(func(t *ynab.Transaction) { t.SubTransactions = []ynab.SubTransaction{{ID: "s"}} }),
			func(m *matcher.Txn) { m.IsSplit = true }},
		{"only deleted subtransactions is not a split",
			with(func(t *ynab.Transaction) { t.SubTransactions = []ynab.SubTransaction{{ID: "s", Deleted: true}} }),
			func(*matcher.Txn) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp := want
			tc.edit(&exp)
			assert.Equal(t, exp, ToMatcherTxn(tc.in))
		})
	}
}

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{"": ModeAuto, "auto": ModeAuto, "always": ModeAlways, "never": ModeNever}
	for in, want := range cases {
		got, err := ParseMode(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"sometimes", "AUTO", "auto\n"} {
		_, err := ParseMode(bad)
		assert.ErrorContains(t, err, "must be one of", bad)
	}
}

func TestNewWriter_validatesConfig(t *testing.T) {
	h := newHarness(t)
	cases := map[string]func(*Config){
		"no clock":   func(c *Config) { c.Now = nil },
		"no flag":    func(c *Config) { c.FlagColor = "" },
		"bad mode":   func(c *Config) { c.SplitInPlace = "maybe" },
		"wrong case": func(c *Config) { c.SplitInPlace = "AUTO" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewWriter(h.api, h.store, nil, h.config(edit))
			assert.Error(t, err)
		})
	}
}

func TestNewWriter_appliesDefaults(t *testing.T) {
	h := newHarness(t)
	w, err := NewWriter(h.api, h.store, nil, h.config(func(c *Config) { c.Logger = nil }))
	require.NoError(t, err)
	assert.Equal(t, DefaultPayee, w.cfg.Payee)
	assert.Equal(t, DefaultStageMaxAgeDays, w.cfg.StageMaxAgeDays)
	assert.Equal(t, ModeAuto, w.cfg.SplitInPlace)
	assert.NotNil(t, w.log)
}

func TestInvalidJobError_message(t *testing.T) {
	err := &InvalidJobError{Key: "k1", Reason: "splits sum to -1, want -2"}
	assert.Equal(t, `sync: invalid charge job "k1": splits sum to -1, want -2`, err.Error())
}
