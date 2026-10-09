package cli

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/postalservice14/itemize-ynab/internal/application/sync"
)

const (
	dryRunBanner = "DRY RUN — no YNAB writes were made"
	maxNoteRunes = 60
	splitIndent  = "    "
)

// statusOrder is the order totals are listed in.
var statusOrder = []sync.Status{
	sync.StatusCategorized, sync.StatusSplitInPlace, sync.StatusNeedsManualMatch,
	sync.StatusStagedForImport, sync.StatusSkipped, sync.StatusAlreadyProcessed, sync.StatusFailed,
}

// plannedStatuses are the outcomes that write to YNAB; a dry run only plans them.
var plannedStatuses = map[sync.Status]bool{
	sync.StatusCategorized: true, sync.StatusSplitInPlace: true,
	sync.StatusNeedsManualMatch: true, sync.StatusStagedForImport: true,
}

// PrintSummary writes the run summary (PRD §6.8) to w: the dry-run banner, the
// charge table, the totals by outcome and, when the run stopped early, the
// reason. With verbose it lists each row's proposed splits under the row.
func PrintSummary(w io.Writer, s sync.Summary, verbose bool) {
	var b strings.Builder
	if s.DryRun {
		b.WriteString(dryRunBanner + "\n\n")
	}
	switch {
	case len(s.Rows) > 0:
		b.WriteString(renderTable(s, verbose))
		b.WriteString("\n" + totalsLine(s) + "\n")
	case s.StoppedEarly == nil:
		b.WriteString("Nothing to do: no charges found.\n")
	}
	if s.StoppedEarly != nil {
		if len(s.Rows) > 0 {
			b.WriteString("\n")
		}
		b.WriteString(stoppedBlock(s))
	}
	_, _ = io.WriteString(w, b.String())
}

func renderTable(s sync.Summary, verbose bool) string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ORDER\tAMOUNT\tOUTCOME\tYNAB TXN\tNOTE")
	for _, r := range s.Rows {
		// Every cell is one line without tabs, so the table has exactly one
		// line per row and the split listing below lands under its row.
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			orDash(oneLine(r.OrderDisplayID)), FormatCents(r.AmountCents), oneLine(outcomeLabel(r, s.DryRun)),
			orDash(oneLine(r.TxnID)), noteText(r))
	}
	_ = tw.Flush()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	var out strings.Builder
	for i, line := range lines {
		out.WriteString(strings.TrimRight(line, " ") + "\n")
		if !verbose || i == 0 {
			continue
		}
		for _, sp := range s.Rows[i-1].Splits {
			fmt.Fprintf(&out, "%s%s → %s\n", splitIndent, oneLine(sp.Category), FormatCents(sp.AmountCents))
		}
	}
	return out.String()
}

func outcomeLabel(r sync.Row, dryRun bool) string {
	if dryRun && plannedStatuses[r.Status] {
		return string(r.Status) + " (planned)"
	}
	return string(r.Status)
}

// noteText is the table note: the full error for a failed row, otherwise the
// note cut to a readable length. Always one line.
func noteText(r sync.Row) string {
	if r.Status == sync.StatusFailed && r.Err != "" {
		return oneLine(r.Err)
	}
	note := []rune(oneLine(r.Note))
	if len(note) > maxNoteRunes {
		return string(note[:maxNoteRunes]) + "..."
	}
	return string(note)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func totalsLine(s sync.Summary) string {
	counts := s.Totals()
	var parts []string
	seen := map[sync.Status]bool{}
	for _, st := range statusOrder {
		seen[st] = true
		if n := counts[st]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, st))
		}
	}
	var extra []string
	for st := range counts {
		if !seen[st] {
			extra = append(extra, string(st))
		}
	}
	sort.Strings(extra)
	for _, st := range extra {
		parts = append(parts, fmt.Sprintf("%d %s", counts[sync.Status(st)], st))
	}
	label := "Totals"
	if s.DryRun {
		label += " (DRY RUN)"
	}
	return fmt.Sprintf("%s: %s (%d total)", label, strings.Join(parts, ", "), len(s.Rows))
}

func stoppedBlock(s sync.Summary) string {
	st := s.StoppedEarly
	var b strings.Builder
	fmt.Fprintf(&b, "STOPPED EARLY: %s\n", oneLine(st.Reason))
	fmt.Fprintf(&b, "  %s\n", stopAdvice(st))
	fmt.Fprintf(&b, "  %d row(s) completed before the stop. The charge or order being processed when the run stopped is not listed above.\n",
		len(s.Rows))
	if st.Err != "" {
		fmt.Fprintf(&b, "  Detail: %s\n", oneLine(st.Err))
	}
	return b.String()
}

const refreshCookies = "refresh your Walmart cookies (`itemize-ynab walmart import-curl`)"

func stopAdvice(st *sync.StopReason) string {
	switch st.Kind {
	case sync.StopKindYNABRateLimit:
		return "YNAB rate limit reached: try again later (200 requests/hour per token)."
	case sync.StopKindWalmartBotChallenge:
		return "Walmart served a bot challenge: " + refreshCookies + ", then try again."
	case sync.StopKindWalmartStaleSession:
		return "Walmart rejected the session (stale cookies): " + refreshCookies + ", then try again."
	case sync.StopKindWalmartRateLimited:
		return "Walmart rate limited the session: wait a while before trying again; if it persists, refresh your Walmart cookies."
	default:
		return "Walmart blocked the session: " + refreshCookies + ", then try again later."
	}
}
