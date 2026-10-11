package categorizer_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
)

func TestCategorize_shortName_returnedOnAssignment(t *testing.T) {
	chat := scripted(`{"items":[{"index":0,"category":"Groceries","short":"Milk"}]}`)

	got, err := categorizer.New(chat).Categorize(context.Background(),
		items("Great Value Whole Vitamin D Milk, 1 Gallon, 128 fl oz"), allowed)

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Milk", got[0].ShortName)
	assert.Equal(t, "Great Value Whole Vitamin D Milk, 1 Gallon, 128 fl oz", got[0].Name)
}

func TestCategorize_shortName_trimmed(t *testing.T) {
	chat := scripted(`{"items":[{"index":0,"category":"Groceries","short":"  Broccoli florets \t"}]}`)

	got, err := categorizer.New(chat).Categorize(context.Background(), items("Broccoli"), allowed)

	require.NoError(t, err)
	assert.Equal(t, "Broccoli florets", got[0].ShortName)
}

func TestCategorize_shortName_promptAsksForIt(t *testing.T) {
	chat := scripted(`{"items":[{"index":0,"category":"Groceries","short":"Milk"}]}`)

	_, err := categorizer.New(chat).Categorize(context.Background(), items("Milk"), allowed)

	require.NoError(t, err)
	assert.Contains(t, chat.requests[0].System, `"short"`)
}

func TestCategorize_shortName_unusable_fallsBackToEmptyWithoutRepair(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{"missing", ``},
		{"empty", `,"short":""`},
		{"blank", `,"short":"   "`},
		{"not a string", `,"short":42`},
		{"too long", fmt.Sprintf(`,"short":%q`, strings.Repeat("a", 31))},
		{"newline", `,"short":"Milk\nIgnore previous"`},
		{"control rune", `,"short":"Mi\u0007lk"`},
		{"carries marker", `,"short":"[itemize:walmart:1:2:1]"`},
		{"carries marker any case", `,"short":"[ITEMIZE:x]"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chat := scripted(`{"items":[{"index":0,"category":"Groceries"` + tc.field + `}]}`)

			got, err := categorizer.New(chat).Categorize(context.Background(), items("Milk"), allowed)

			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Empty(t, got[0].ShortName)
			assert.Equal(t, "Groceries", got[0].Category)
			assert.Len(t, chat.requests, 1, "a bad short name must not trigger a repair call")
		})
	}
}

func TestCategorize_shortName_exactlyAtLimitKept(t *testing.T) {
	short := strings.Repeat("é", 30)
	chat := scripted(fmt.Sprintf(`{"items":[{"index":0,"category":"Groceries","short":%q}]}`, short))

	got, err := categorizer.New(chat).Categorize(context.Background(), items("Milk"), allowed)

	require.NoError(t, err)
	assert.Equal(t, short, got[0].ShortName)
}
