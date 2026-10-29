package dashboard

import (
	"fmt"
	"strconv"
	"strings"
)

// baseUnitsPerCoin is 10^8: the node keeps amounts as uint64 base units and pays 50 coins of
// them per block at first (ADR-0015). The dashboard shows coins; the exact count stays in a title.
const baseUnitsPerCoin = 100_000_000

// coins formats base units as coins: thousands grouped, up to 8 decimals, no trailing zeros.
// Integer math only: a float loses precision above 2^53 and amounts are uint64.
func coins(units uint64) string {
	out := group(units / baseUnitsPerCoin)

	if frac := units % baseUnitsPerCoin; frac != 0 {
		out += "." + strings.TrimRight(fmt.Sprintf("%08d", frac), "0")
	}

	return out
}

// baseUnits formats an exact amount with thousands grouped, for the hover title.
func baseUnits(units uint64) string { return group(units) }

func group(n uint64) string {
	digits := strconv.FormatUint(n, 10)

	var b strings.Builder

	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}

		b.WriteRune(c)
	}

	return b.String()
}
