package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/application/sync"
	"github.com/postalservice14/itemize-ynab/internal/cli"
)

func printed(s sync.Summary, verbose bool) string {
	var b bytes.Buffer
	cli.PrintSummary(&b, s, verbose)
	return b.String()
}

func mixedRows() []sync.Row {
	return []sync.Row{
		{OrderDisplayID: "A-100", AmountCents: 500, Status: sync.StatusCategorized, TxnID: "tA",
			Splits: []sync.SplitView{{Category: "Groceries", AmountCents: 500}}},
		{OrderDisplayID: "B-200", AmountCents: 7350, Status: sync.StatusSplitInPlace, TxnID: "tB1",
			Splits: []sync.SplitView{{Category: "Household", AmountCents: 4410}, {Category: "Groceries", AmountCents: 2940}}},
		{OrderDisplayID: "C-300", AmountCents: -1299, Status: sync.StatusSkipped, Note: "refund: returned item"},
		{OrderDisplayID: "D-400", AmountCents: 18583, Status: sync.StatusFailed, Err: `no category for "Towels"`},
		{OrderDisplayID: "E-500", AmountCents: 100, Status: sync.StatusAlreadyProcessed, TxnID: "tE", Note: "recorded as categorized"},
	}
}

func TestPrintSummary_normalRunGolden(t *testing.T) {
	got := printed(sync.Summary{Rows: mixedRows()}, false)

	want := `ORDER  AMOUNT   OUTCOME            YNAB TXN  NOTE
A-100  $5.00    categorized        tA
B-200  $73.50   split_in_place     tB1
C-300  -$12.99  skipped            -         refund: returned item
D-400  $185.83  failed             -         no category for "Towels"
E-500  $1.00    already_processed  tE        recorded as categorized

Totals: 1 categorized, 1 split_in_place, 1 skipped, 1 already_processed, 1 failed (5 total)
`
	assert.Equal(t, want, got)
}

func TestPrintSummary_dryRunBannerAndLabels(t *testing.T) {
	rows := []sync.Row{
		{OrderDisplayID: "A-100", AmountCents: 500, Status: sync.StatusCategorized, TxnID: "tA"},
		{OrderDisplayID: "C-300", AmountCents: 300, Status: sync.StatusSkipped, Note: "no matching YNAB transaction"},
	}

	got := printed(sync.Summary{DryRun: true, Rows: rows}, false)

	want := `DRY RUN — no YNAB writes were made

ORDER  AMOUNT  OUTCOME                YNAB TXN  NOTE
A-100  $5.00   categorized (planned)  tA
C-300  $3.00   skipped                -         no matching YNAB transaction

Totals (DRY RUN): 1 categorized, 1 skipped (2 total)
`
	assert.Equal(t, want, got)
}

func TestPrintSummary_dryRunBannerEvenWhenEmpty(t *testing.T) {
	got := printed(sync.Summary{DryRun: true}, false)

	assert.Equal(t, "DRY RUN — no YNAB writes were made\n\nNothing to do: no charges found.\n", got)
}

func TestPrintSummary_emptyRun(t *testing.T) {
	assert.Equal(t, "Nothing to do: no charges found.\n", printed(sync.Summary{}, false))
}

func TestPrintSummary_stoppedEarlyYNABGolden(t *testing.T) {
	s := sync.Summary{
		Rows: mixedRows()[:2],
		StoppedEarly: &sync.StopReason{Source: sync.StopYNABRateLimit, Kind: sync.StopKindYNABRateLimit,
			Reason: "YNAB rate limit", Err: "update transaction walmart:oC:500:1: ynab: rate limited"},
	}

	got := printed(s, false)

	want := `ORDER  AMOUNT  OUTCOME         YNAB TXN  NOTE
A-100  $5.00   categorized     tA
B-200  $73.50  split_in_place  tB1

Totals: 1 categorized, 1 split_in_place (2 total)

STOPPED EARLY: YNAB rate limit
  YNAB rate limit reached: try again later (200 requests/hour per token).
  2 row(s) completed before the stop. The charge or order being processed when the run stopped is not listed above.
  Detail: update transaction walmart:oC:500:1: ynab: rate limited
`
	assert.Equal(t, want, got)
}

func TestPrintSummary_stoppedEarlyWalmartReasons(t *testing.T) {
	tests := map[string]struct {
		kind   sync.StopKind
		reason string
		want   []string
	}{
		"bot challenge": {sync.StopKindWalmartBotChallenge, "Walmart blocked: bot challenge",
			[]string{"bot challenge", "refresh your Walmart cookies", "import-curl"}},
		"stale session": {sync.StopKindWalmartStaleSession, "Walmart blocked: stale session",
			[]string{"stale", "refresh your Walmart cookies", "import-curl"}},
		"rate limited": {sync.StopKindWalmartRateLimited, "Walmart blocked: rate limited",
			[]string{"rate limited", "wait", "refresh your Walmart cookies"}},
		"unknown kind": {sync.StopKindOther, "Walmart blocked",
			[]string{"Walmart blocked the session", "refresh your Walmart cookies"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := printed(sync.Summary{StoppedEarly: &sync.StopReason{
				Source: sync.StopWalmartBlocked, Kind: tc.kind, Reason: tc.reason, Err: "walmart: blocked"}}, false)

			assert.Contains(t, got, "STOPPED EARLY: "+tc.reason)
			assert.Contains(t, got, "0 row(s) completed before the stop")
			assert.NotContains(t, got, "Nothing to do")
			assert.NotContains(t, got, "ORDER")
			for _, w := range tc.want {
				assert.Contains(t, got, w)
			}
		})
	}
}

func TestPrintSummary_stopAdviceFollowsKindNotReasonText(t *testing.T) {
	tests := map[string]struct {
		kind sync.StopKind
		want string
	}{
		"bot challenge": {sync.StopKindWalmartBotChallenge, "  Walmart served a bot challenge: "},
		"stale session": {sync.StopKindWalmartStaleSession, "  Walmart rejected the session (stale cookies): "},
		"rate limited":  {sync.StopKindWalmartRateLimited, "  Walmart rate limited the session: "},
		"ynab":          {sync.StopKindYNABRateLimit, "  YNAB rate limit reached: "},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := printed(sync.Summary{StoppedEarly: &sync.StopReason{
				Source: sync.StopWalmartBlocked, Kind: tc.kind, Reason: "blocked (HTTP 456), see detail"}}, false)

			assert.Contains(t, got, "STOPPED EARLY: blocked (HTTP 456), see detail\n"+tc.want)
		})
	}
}

func TestPrintSummary_stoppedEarlyDryRunKeepsBanner(t *testing.T) {
	got := printed(sync.Summary{DryRun: true, Rows: mixedRows()[:1], StoppedEarly: &sync.StopReason{
		Source: sync.StopYNABRateLimit, Kind: sync.StopKindYNABRateLimit, Reason: "YNAB rate limit"}}, false)

	assert.True(t, strings.HasPrefix(got, "DRY RUN — no YNAB writes were made\n"))
	assert.Contains(t, got, "Totals (DRY RUN):")
	assert.Contains(t, got, "STOPPED EARLY: YNAB rate limit")
	assert.NotContains(t, got, "Detail:")
}

func TestPrintSummary_verboseListsSplitsUnderRows(t *testing.T) {
	got := printed(sync.Summary{Rows: mixedRows()[:3]}, true)

	want := `ORDER  AMOUNT   OUTCOME         YNAB TXN  NOTE
A-100  $5.00    categorized     tA
    Groceries → $5.00
B-200  $73.50   split_in_place  tB1
    Household → $44.10
    Groceries → $29.40
C-300  -$12.99  skipped         -         refund: returned item

Totals: 1 categorized, 1 split_in_place, 1 skipped (3 total)
`
	assert.Equal(t, want, got)
	assert.NotContains(t, printed(sync.Summary{Rows: mixedRows()[:3]}, false), "→")
}

func TestPrintSummary_verboseWithNewlinesInCells_staysAligned(t *testing.T) {
	rows := []sync.Row{
		{OrderDisplayID: "A-\n100", AmountCents: 500, Status: sync.StatusCategorized, TxnID: "t\nA",
			Splits: []sync.SplitView{{Category: "Groceries", AmountCents: 500}}},
		{OrderDisplayID: "B-200", AmountCents: 300, Status: sync.StatusCategorized, TxnID: "tB\r\n",
			Splits: []sync.SplitView{{Category: "Household", AmountCents: 300}}},
	}

	var got string
	require.NotPanics(t, func() { got = printed(sync.Summary{Rows: rows}, true) })

	want := `ORDER   AMOUNT  OUTCOME      YNAB TXN  NOTE
A- 100  $5.00   categorized  t A
    Groceries → $5.00
B-200   $3.00   categorized  tB
    Household → $3.00

Totals: 2 categorized (2 total)
`
	assert.Equal(t, want, got)
}

func TestPrintSummary_notesAreTruncatedExceptFailures(t *testing.T) {
	long := strings.Repeat("n", 200)
	rows := []sync.Row{
		{OrderDisplayID: "A", AmountCents: 100, Status: sync.StatusSkipped, Note: long},
		{OrderDisplayID: "B", AmountCents: 100, Status: sync.StatusFailed, Err: long},
		{OrderDisplayID: "C", AmountCents: 100, Status: sync.StatusSkipped, Note: "line one\nline two"},
	}

	got := printed(sync.Summary{Rows: rows}, false)

	assert.Contains(t, got, strings.Repeat("n", 60)+"...")
	assert.NotContains(t, got, strings.Repeat("n", 61)+"...", "skipped note cut at 60")
	assert.Contains(t, got, strings.Repeat("n", 200), "failed rows keep the full error")
	assert.Contains(t, got, "line one line two")
}

func TestPrintSummary_amountsUseTheOneFormatter(t *testing.T) {
	rows := []sync.Row{
		{OrderDisplayID: "Z", AmountCents: 0, Status: sync.StatusSkipped},
		{OrderDisplayID: "O", AmountCents: 1, Status: sync.StatusSkipped},
		{OrderDisplayID: "L", AmountCents: 123456789, Status: sync.StatusSkipped},
	}

	got := printed(sync.Summary{Rows: rows}, false)

	for _, want := range []string{"$0.00", "$0.01", "$1234567.89"} {
		assert.Contains(t, got, want)
	}
}

func TestPrintSummary_emptyFieldsPrintDash(t *testing.T) {
	got := printed(sync.Summary{Rows: []sync.Row{{AmountCents: 100, Status: sync.StatusSkipped}}}, false)

	assert.Contains(t, got, "-      $1.00")
}
