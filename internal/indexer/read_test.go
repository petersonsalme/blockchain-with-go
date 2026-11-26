package indexer_test

import (
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
)

func TestShareString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		balance string
		total   string
		want    string
	}{
		{name: "zero_total", balance: "100", total: "0", want: "0.0000"},
		{name: "zero_balance", balance: "0", total: "100", want: "0.0000"},
		{name: "both_zero", balance: "0", total: "0", want: "0.0000"},
		{name: "full_share", balance: "100", total: "100", want: "1.0000"},
		{name: "quarter_share", balance: "25", total: "100", want: "0.2500"},
		{name: "repeating_third_truncated", balance: "1", total: "3", want: "0.3333"},
		{name: "tiny_non_zero_truncated", balance: "1", total: "20000", want: "0.0000"},
		{name: "uint64_max_full_share", balance: "18446744073709551615", total: "18446744073709551615", want: "1.0000"},
		{name: "above_max_int64_half_share", balance: "9223372036854775808", total: "18446744073709551616", want: "0.5000"},
		{name: "empty_balance", balance: "", total: "100", want: "0.0000"},
		{name: "empty_total", balance: "100", total: "", want: "0.0000"},
		{name: "non_numeric_balance", balance: "abc", total: "100", want: "0.0000"},
		{name: "non_numeric_total", balance: "100", total: "xyz", want: "0.0000"},
		{name: "negative_balance", balance: "-1", total: "100", want: "0.0000"},
		{name: "negative_total", balance: "100", total: "-1", want: "0.0000"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := indexer.ShareString(tc.balance, tc.total)
			if got != tc.want {
				t.Fatalf("ShareString(%q, %q) = %q, want %q", tc.balance, tc.total, got, tc.want)
			}
		})
	}
}
