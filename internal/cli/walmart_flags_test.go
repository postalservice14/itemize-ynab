package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWalmartFlags_valid(t *testing.T) {
	tests := map[string]struct {
		args []string
		want walmartOpts
		pos  []string
	}{
		"defaults":         {nil, walmartOpts{Days: 14}, nil},
		"dry run":          {[]string{"-dry-run"}, walmartOpts{Days: 14, DryRun: true}, nil},
		"days":             {[]string{"-days", "7"}, walmartOpts{Days: 7}, nil},
		"days equals":      {[]string{"-days=30"}, walmartOpts{Days: 30}, nil},
		"max":              {[]string{"-max", "5"}, walmartOpts{Days: 14, Max: 5}, nil},
		"force":            {[]string{"-force"}, walmartOpts{Days: 14, Force: true}, nil},
		"verbose":          {[]string{"-verbose"}, walmartOpts{Days: 14, Verbose: true}, nil},
		"double dash form": {[]string{"--dry-run", "--days", "3"}, walmartOpts{Days: 3, DryRun: true}, nil},
		"all flags":        {[]string{"-dry-run", "-days", "2", "-max", "1", "-force", "-verbose"}, walmartOpts{Days: 2, Max: 1, DryRun: true, Force: true, Verbose: true}, nil},
		"positional between flags": {
			[]string{"-days", "9", "import-curl", "-verbose"},
			walmartOpts{Days: 9, Verbose: true}, []string{"import-curl"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, pos, err := parseWalmartFlags(tc.args)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.pos, pos)
		})
	}
}

func TestParseWalmartFlags_invalid(t *testing.T) {
	tests := map[string][]string{
		"unknown flag":      {"-nope"},
		"zero days":         {"-days", "0"},
		"negative days":     {"-days", "-3"},
		"negative max":      {"-max", "-1"},
		"non-numeric days":  {"-days", "soon"},
		"days needs value":  {"-days"},
		"non-numeric max":   {"-max", "x"},
		"bool with garbage": {"-dry-run=maybe"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := parseWalmartFlags(args)

			require.ErrorIs(t, err, errUsage)
		})
	}
}
