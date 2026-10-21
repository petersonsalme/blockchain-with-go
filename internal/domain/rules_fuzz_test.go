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
	sec = sec % span
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
		for i := 0; i < int(nTx); i++ {
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

		limit := domain.DefaultMaxBlockBytes
		if maxBytes > 0 {
			limit = int(maxBytes)
		}
		t.Setenv(domain.MaxBlockBytesEnvVar, strconv.Itoa(int(maxBytes)))

		err := domain.ValidateBlockRules(b, parent, now)

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
		case ts.Before(parent.Timestamp):
			wantCode = domain.CodeTimestampBeforeParent
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
