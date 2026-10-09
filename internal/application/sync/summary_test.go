package sync

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/postalservice14/itemize-ynab/internal/adapters/ynab"
	"github.com/postalservice14/itemize-ynab/internal/domain/order"
	"github.com/postalservice14/itemize-ynab/internal/infrastructure/config"
)

func TestSummaryExitCode(t *testing.T) {
	stop := &StopReason{Source: StopYNABRateLimit, Reason: "YNAB rate limit"}
	cases := map[string]struct {
		sum  Summary
		want int
	}{
		"empty run":                {Summary{}, 0},
		"all good or skipped":      {Summary{Rows: []Row{{Status: StatusCategorized}, {Status: StatusSkipped}, {Status: StatusAlreadyProcessed}}}, 0},
		"manual match is not fail": {Summary{Rows: []Row{{Status: StatusNeedsManualMatch}, {Status: StatusStagedForImport}}}, 0},
		"one failed":               {Summary{Rows: []Row{{Status: StatusCategorized}, {Status: StatusFailed}}}, 2},
		"stopped early":            {Summary{StoppedEarly: stop}, 3},
		"stopped beats failed":     {Summary{Rows: []Row{{Status: StatusFailed}}, StoppedEarly: stop}, 3},
		"dry run same rules":       {Summary{DryRun: true, Rows: []Row{{Status: StatusFailed}}}, 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.sum.ExitCode())
		})
	}
}

func TestSummaryTotals(t *testing.T) {
	sum := Summary{Rows: []Row{
		{Status: StatusCategorized}, {Status: StatusCategorized}, {Status: StatusSkipped},
		{Status: StatusFailed}, {Status: StatusSplitInPlace},
	}}

	assert.Equal(t, map[Status]int{
		StatusCategorized: 2, StatusSkipped: 1, StatusFailed: 1, StatusSplitInPlace: 1,
	}, sum.Totals())
	assert.Empty(t, Summary{}.Totals())
}

func TestExitCodeForError(t *testing.T) {
	rateLimited := &ynab.APIError{Status: 429}
	cases := map[string]struct {
		err  error
		want int
	}{
		"nil":                {nil, 0},
		"invalid config":     {fmt.Errorf("x: %w", config.ErrInvalid), 1},
		"ynab unauthorized":  {&ynab.APIError{Status: 401}, 1},
		"ynab forbidden":     {&ynab.APIError{Status: 403}, 1},
		"ynab rate limited":  {fmt.Errorf("categories: %w", rateLimited), 3},
		"walmart blocked":    {&order.BlockedError{Kind: order.BotChallenge}, 3},
		"canceled":           {context.Canceled, 1},
		"anything else":      {errors.New("boom"), 1},
		"ynab server error":  {&ynab.APIError{Status: 500}, 1},
		"wrapped rate limit": {fmt.Errorf("a: %w", fmt.Errorf("b: %w", rateLimited)), 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ExitCodeForError(tc.err))
		})
	}
}
