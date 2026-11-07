package domain_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// fuzzTime maps fuzz integers to an instant whose year spans roughly
// -1000..12000, so both valid and unserializable (outside 0..9999) years occur.
func fuzzTime(sec, nsec int64) time.Time {
	const span = 13000 * 365 * 24 * 3600

	sec %= span

	return time.Unix(sec+2*span/13, nsec%1_000_000_000).UTC()
}

func FuzzValidateBlockRules(f *testing.F) {
	f.Add(int64(1_700_000_000), int64(0), int64(1_699_999_000), int64(0), int64(1_700_000_100), int64(0), 1, uint8(2), uint8(10), uint16(0))
	f.Add(int64(1_700_000_000), int64(0), int64(1_700_000_001), int64(0), int64(1_700_000_100), int64(0), 1, uint8(0), uint8(0), uint16(0))
	f.Add(int64(1_700_000_000), int64(0), int64(1_699_999_000), int64(0), int64(1_600_000_000), int64(0), 1, uint8(1), uint8(1), uint16(0))
	f.Add(int64(1_700_000_000), int64(0), int64(1_699_999_000), int64(0), int64(1_700_000_100), int64(0), 1, uint8(3), uint8(50), uint16(300))
	f.Add(int64(1_700_000_000), int64(0), int64(1_699_999_000), int64(0), int64(1_700_000_100), int64(0), 1, uint8(1), uint8(3), uint16(0))
	f.Add(int64(1<<40), int64(0), int64(0), int64(0), int64(0), int64(0), 0, uint8(0), uint8(0), uint16(0))

	f.Fuzz(func(t *testing.T, sec, nsec, parentSec, parentNsec, nowSec, nowNsec int64, index int, nTx, txSize uint8, maxBytes uint16) {
		ts := fuzzTime(sec, nsec)
		parent := &domain.Block{Index: index - 1, Timestamp: fuzzTime(parentSec, parentNsec)}
		now := fuzzTime(nowSec, nowNsec)

		b := &domain.Block{Index: index, Timestamp: ts, PrevHash: parent.Hash}
		for i := range nTx {
			b.Transactions = append(b.Transactions, domain.Transaction{
				Timestamp: ts,
				Outputs:   []domain.TxOut{{Amount: uint64(i), Address: strings.Repeat("a", int(txSize)*8)}},
			})
		}

		b.MerkleRoot = domain.MerkleRoot(b.Transactions)

		corrupt := txSize%7 == 3
		if corrupt {
			b.MerkleRoot = strings.Repeat("0", 64)
		}

		// The rule is the constant whatever the removed variable holds (ADR-0032).
		limit := domain.MaxBlockBytes

		t.Setenv("MAX_BLOCK_BYTES", strconv.Itoa(int(maxBytes)))

		err := domain.ValidateBlockRules(b, []*domain.Block{parent}, now)

		encoded, marshalErr := json.Marshal(b)
		if marshalErr != nil {
			if err == nil {
				t.Fatal("unserializable block was accepted")
			}

			return
		}

		var wantCode string

		switch {
		case len(encoded) > limit:
			wantCode = domain.CodeBlockTooLarge
		case corrupt:
			wantCode = domain.CodeMerkleRootMismatch
		case !ts.After(domain.MedianTimePast([]*domain.Block{parent})):
			wantCode = domain.CodeTimestampNotAfterMedianTimePast
		case ts.After(now.Add(domain.MaxFutureDrift)):
			wantCode = domain.CodeTimestampTooFarInTheFuture
		}

		if wantCode == "" {
			if err != nil {
				t.Fatalf("block accepted by the oracle was rejected: %v", err)
			}

			return
		}

		var verr *domain.ValidationError
		if !errors.As(err, &verr) {
			t.Fatalf("want *ValidationError %q, got %v", wantCode, err)
		}

		if verr.Code != wantCode {
			t.Fatalf("code %q, want %q", verr.Code, wantCode)
		}
	})
}

// FuzzValidateTimestamp checks ValidateTimestamp against an independent oracle
// over histories of 1..30 blocks with arbitrary, unsorted timestamps.
func FuzzValidateTimestamp(f *testing.F) {
	f.Add(int64(1), int64(2), int64(3), int64(4), int64(5), 12, int64(3), int64(100))
	f.Add(int64(100), int64(0), int64(50), int64(7), int64(9), 30, int64(50), int64(60))
	f.Add(int64(0), int64(0), int64(0), int64(0), int64(0), 1, int64(0), int64(0))
	f.Add(int64(5), int64(9), int64(1), int64(8), int64(2), 11, int64(100), int64(0))

	f.Fuzz(func(t *testing.T, a, b, c, d, e int64, n int, tsSec, nowSec int64) {
		n = 1 + (n%30+30)%30
		seeds := []int64{a, b, c, d, e}
		history := make([]*domain.Block, n)

		for i := range history {
			history[i] = &domain.Block{Timestamp: time.Unix(1_700_000_000+seeds[i%5]%1000+int64(i*7%13), 0).UTC()}
		}

		ts := time.Unix(1_700_000_000+tsSec%2000, 0).UTC()
		now := time.Unix(1_700_000_000+nowSec%20000, 0).UTC()

		window := history
		if len(window) > 11 {
			window = window[len(window)-11:]
		}

		stamps := make([]time.Time, len(window))
		for i, blk := range window {
			stamps[i] = blk.Timestamp
		}

		// Independent insertion sort, not the production sort.
		for i := 1; i < len(stamps); i++ {
			for j := i; j > 0 && stamps[j].Before(stamps[j-1]); j-- {
				stamps[j], stamps[j-1] = stamps[j-1], stamps[j]
			}
		}

		median := stamps[len(stamps)/2]

		var wantCode string

		switch {
		case !ts.After(median):
			wantCode = domain.CodeTimestampNotAfterMedianTimePast
		case ts.After(now.Add(domain.MaxFutureDrift)):
			wantCode = domain.CodeTimestampTooFarInTheFuture
		}

		err := domain.ValidateTimestamp(&domain.Block{Timestamp: ts}, history, now)
		if wantCode == "" {
			if err != nil {
				t.Fatalf("accepted by the oracle, rejected: %v", err)
			}

			return
		}

		var verr *domain.ValidationError
		if !errors.As(err, &verr) || verr.Code != wantCode {
			t.Fatalf("want code %q, got %v", wantCode, err)
		}
	})
}
