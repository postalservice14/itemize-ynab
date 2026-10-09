package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
)

func listCategories(ctx context.Context, c *ynab.Client, w io.Writer, eligibleOnly bool) error {
	cats, err := c.ListCategories(ctx)
	if err != nil {
		return err
	}
	if eligibleOnly {
		cats = ynab.EligibleCategories(cats)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CATEGORY\tGROUP\tID\t")
	for _, cat := range cats {
		if cat.Deleted {
			continue
		}
		marker := ""
		if cat.Hidden {
			marker = "(hidden)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", cat.Name, cat.GroupName, cat.ID, marker)
	}
	return tw.Flush()
}

func listAccounts(ctx context.Context, c *ynab.Client, w io.Writer) error {
	accts, err := c.ListAccounts(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ACCOUNT\tID\tTYPE\t")
	for _, a := range accts {
		if a.Deleted {
			continue
		}
		marker := ""
		if a.Closed {
			marker = "(closed)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Name, a.ID, a.Type, marker)
	}
	return tw.Flush()
}
