package pow

import (
	"encoding/hex"
	"fmt"
	"testing"
)

// hashWithZeroBits builds the 64-character hash whose first k bits are zero
// and whose remaining bits are one, so it has exactly k leading zero bits.
func hashWithZeroBits(k int) string {
	var raw [32]byte
	for bit := k; bit < 256; bit++ {
		raw[bit/8] |= 0x80 >> (bit % 8)
	}

	return hex.EncodeToString(raw[:])
}

func TestSolvesDifficultyCountsLeadingZeroBits(t *testing.T) {
	for _, k := range []int{0, 1, 3, 4, 5, 8, 255, 256} {
		hash := hashWithZeroBits(k)
		for _, d := range []int{-1, 0, 1, k - 1, k} {
			if d > k {
				continue
			}

			if !solvesDifficulty(hash, d) {
				t.Errorf("hash with %d zero bits (%s) must solve d=%d", k, hash, d)
			}
		}

		if solvesDifficulty(hash, k+1) {
			t.Errorf("hash with %d zero bits (%s) must not solve d=%d", k, hash, k+1)
		}
	}
}

func TestSolvesDifficultyBounds(t *testing.T) {
	ones := hashWithZeroBits(0)
	zeros := hashWithZeroBits(256)

	for _, d := range []int{-100, -1, 0} {
		if !solvesDifficulty(ones, d) {
			t.Errorf("d=%d must always solve", d)
		}
	}

	for _, d := range []int{257, 1000} {
		if solvesDifficulty(zeros, d) {
			t.Errorf("d=%d must never solve, even for the all-zero hash", d)
		}
	}
}

func TestSolvesDifficultyRejectsMalformedHashes(t *testing.T) {
	for name, hash := range map[string]string{
		"empty":     "",
		"short":     "00ff",
		"long":      hashWithZeroBits(256) + "0",
		"uppercase": "00000000000000000000000000000000000000000000000000000000000000F0",
		"not hex":   "zz00000000000000000000000000000000000000000000000000000000000000",
	} {
		if solvesDifficulty(hash, 1) {
			t.Errorf("%s hash %q must never solve", name, hash)
		}
	}
}

func TestSolvesDifficultyIsMonotonic(t *testing.T) {
	for k := 0; k <= 256; k++ {
		hash := hashWithZeroBits(k)
		for d := 1; d <= 258; d++ {
			if solvesDifficulty(hash, d) && !solvesDifficulty(hash, d-1) {
				t.Fatalf("k=%d: solves d=%d but not d=%d", k, d, d-1)
			}
		}
	}
}

func TestDefaults(t *testing.T) {
	got := fmt.Sprint(DefaultDifficulty, MinDifficulty, MaxDifficulty, DefaultTargetBlockTime)
	if want := "8 1 32 2s"; got != want {
		t.Errorf("defaults = %q, want %q", got, want)
	}
}
