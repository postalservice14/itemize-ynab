package sync

import (
	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/matcher"
)

// ToMatcherTxn converts a YNAB transaction into the matcher's input type.
// matcher.Txn has one import-payee field, so the bank's original import payee
// is preferred and YNAB's cleaned import payee is the fallback.
func ToMatcherTxn(t ynab.Transaction) matcher.Txn {
	importPayee := t.ImportPayeeNameOriginal
	if importPayee == "" {
		importPayee = t.ImportPayeeName
	}
	return matcher.Txn{
		ID:              t.ID,
		AccountID:       t.AccountID,
		PayeeName:       t.PayeeName,
		ImportPayeeName: importPayee,
		Memo:            t.Memo,
		AmountMilli:     t.Amount,
		Date:            t.Date.Time(),
		Deleted:         t.Deleted,
		IsTransfer:      t.IsTransfer(),
		IsSplit:         t.IsSplit(),
	}
}
