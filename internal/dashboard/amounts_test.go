package dashboard

import "testing"

func TestCoinsFormatsBaseUnitsAsCoins(t *testing.T) {
	t.Parallel()

	for units, want := range map[uint64]string{
		0:                          "0",
		1:                          "0.00000001",
		42:                         "0.00000042",
		4_200:                      "0.000042",
		150_000_000:                "1.5",
		99_999_999:                 "0.99999999",
		100_000_000:                "1",
		5_000_000_000:              "50",
		5_000_000_001:              "50.00000001",
		125_000_000_000:            "1,250",
		125_000_000_050:            "1,250.0000005",
		1_000_000_000_000_00:       "1,000,000",
		18_446_744_073_709_551_615: "184,467,440,737.09551615",
	} {
		if got := coins(units); got != want {
			t.Errorf("coins(%d) = %q, want %q", units, got, want)
		}
	}
}

func TestBaseUnitsGroupsThousands(t *testing.T) {
	t.Parallel()

	for units, want := range map[uint64]string{0: "0", 999: "999", 1_000: "1,000", 5_000_000_000: "5,000,000,000"} {
		if got := baseUnits(units); got != want {
			t.Errorf("baseUnits(%d) = %q, want %q", units, got, want)
		}
	}
}

func TestFeeRateFormatsTwoDecimalsRoundedDown(t *testing.T) {
	t.Parallel()

	cases := []struct {
		fee     uint64
		size    int
		wantStr string
		wantOK  bool
	}{
		{4200, 300, "14.00", true},
		{1000, 300, "3.33", true},
		{1, 3, "0.33", true},
		{2, 3, "0.66", true},
		{1000, 3, "333.33", true},
		{0, 250, "0.00", true},
		{5, 1, "5.00", true},
		{18446744073709551615, 1, "18,446,744,073,709,551,615.00", true},
		{1234567, 1, "1,234,567.00", true},
		{10, 0, "", false},
		{10, -1, "", false},
	}

	for _, tc := range cases {
		gotStr, gotOK := feeRate(tc.fee, tc.size)
		if gotStr != tc.wantStr || gotOK != tc.wantOK {
			t.Errorf("feeRate(%d, %d) = (%q, %t), want (%q, %t)", tc.fee, tc.size, gotStr, gotOK, tc.wantStr, tc.wantOK)
		}
	}
}
