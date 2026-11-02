package server

import (
	"math"
	"strconv"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

func TestScheduledIssuance(t *testing.T) {
	tests := []struct {
		height int
		want   uint64
	}{
		{height: -1, want: 0},
		{height: 0, want: 0},
		{height: 1, want: 5_000_000_000},
		{height: 2, want: 10_000_000_000},
		{height: 209_999, want: 1_049_995_000_000_000},
		{height: 210_000, want: 1_049_997_500_000_000},
		{height: 420_000, want: 1_574_996_250_000_000},
	}

	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.height), func(t *testing.T) {
			got := scheduledIssuance(tc.height)
			if got != tc.want {
				t.Fatalf("scheduledIssuance(%d) = %d, want %d", tc.height, got, tc.want)
			}
		})
	}
}

func TestScheduledIssuanceStep(t *testing.T) {
	steps := []int{1, 2, 209_998, 209_999, 210_000, 210_001, 419_999, 420_000, 420_001, 6_929_999, 6_930_000}

	for _, h := range steps {
		got := scheduledIssuance(h) - scheduledIssuance(h-1)
		want := domain.Subsidy(h)

		if got != want {
			t.Fatalf("step at %d: got %d, want %d", h, got, want)
		}
	}
}

func TestScheduledIssuanceCap(t *testing.T) {
	const wantCap uint64 = 2_099_994_997_690_000

	cases := []struct {
		name   string
		height int
	}{
		{name: "6929999", height: 6_929_999},
		{name: "6930000", height: 6_930_000},
		{name: "MaxInt", height: math.MaxInt},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scheduledIssuance(tc.height)
			if got != wantCap {
				t.Fatalf("scheduledIssuance(%s) = %d, want %d", tc.name, got, wantCap)
			}
		})
	}
}
