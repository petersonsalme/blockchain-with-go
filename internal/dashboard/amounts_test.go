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
