package allocator

import (
	"errors"
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sum(parts []int64) int64 {
	var s int64
	for _, p := range parts {
		s += p
	}
	return s
}

func TestAllocate_table(t *testing.T) {
	ones := make([]int64, 50)
	for i := range ones {
		ones[i] = 1
	}
	// 50 equal weights over 7: the first 7 indexes win the tie-break.
	wantTiny := make([]int64, 50)
	for i := 0; i < 7; i++ {
		wantTiny[i] = 1
	}

	tests := []struct {
		name    string
		total   int64
		weights []int64
		want    []int64
	}{
		{"one cent two weights", 1, []int64{1, 1}, []int64{1, 0}},
		{"one cent favors larger remainder", 1, []int64{1, 3}, []int64{0, 1}},
		{"many tiny categories", 7, ones, wantTiny},
		{"zero weight gets nothing", 100, []int64{0, 1, 1}, []int64{0, 50, 50}},
		{"zero weight with remainder", 101, []int64{0, 1, 1}, []int64{0, 51, 50}},
		{"all zero falls back to even", 10, []int64{0, 0, 0}, []int64{4, 3, 3}},
		{"single weight", 1234, []int64{5}, []int64{1234}},
		{"exact division", 100, []int64{1, 1, 3}, []int64{20, 20, 60}},
		{"total zero", 0, []int64{3, 4}, []int64{0, 0}},
		{"total zero empty", 0, nil, []int64{}},
		{"proportional tax example", 10850, []int64{6000, 4000}, []int64{6510, 4340}},
		{"ties break to lowest index", 2, []int64{1, 1, 1}, []int64{1, 1, 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Allocate(tc.total, tc.weights)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.total, sum(got))
		})
	}
}

func TestAllocate_errors(t *testing.T) {
	tests := []struct {
		name    string
		total   int64
		weights []int64
		target  error
	}{
		{"negative total", -1, []int64{1}, ErrNegativeTotal},
		{"negative weight", 5, []int64{1, -1}, ErrNegativeWeight},
		{"empty weights", 5, nil, ErrNoWeights},
		{"weight sum overflow", 5, []int64{math.MaxInt64, 1}, ErrWeightOverflow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Allocate(tc.total, tc.weights)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.target), "got %v", err)
			assert.Nil(t, got)
		})
	}
}

func TestAllocate_largeInputsDoNotOverflow(t *testing.T) {
	total := int64(math.MaxInt64 / 2)
	weights := []int64{math.MaxInt64 / 4, math.MaxInt64 / 4, math.MaxInt64 / 4}
	got, err := Allocate(total, weights)
	require.NoError(t, err)
	assert.Equal(t, total, sum(got))
	for _, p := range got {
		assert.GreaterOrEqual(t, p, int64(0))
	}
}

func TestAllocate_propertySumInvariant(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test data
	for i := 0; i < 5000; i++ {
		n := rng.Intn(20) + 1
		weights := make([]int64, n)
		for j := range weights {
			if rng.Intn(4) != 0 {
				weights[j] = rng.Int63n(1_000_000)
			}
		}
		total := rng.Int63n(10_000_000)
		got, err := Allocate(total, weights)
		require.NoError(t, err)
		require.Len(t, got, n)
		require.Equal(t, total, sum(got), "weights=%v total=%d", weights, total)
		for j, p := range got {
			require.GreaterOrEqual(t, p, int64(0))
			if weights[j] == 0 && sum(weights) != 0 {
				require.Zero(t, p, "zero weight must get zero")
			}
		}
	}
}
