package domain_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func FuzzBlockJSONRoundTrip(f *testing.F) {
	f.Add([]byte(`{"Index":1,"Timestamp":"2026-10-16T12:05:00Z","Transactions":[{"ID":"3a7b","Inputs":[{"PrevTxID":"ab","OutIndex":0,"PublicKey":"cd","Signature":"ef"}],"Outputs":[{"Amount":60,"Address":"addr"}],"Timestamp":"2026-10-16T12:05:00Z"}],"Hash":"00f4","PrevHash":"00a1","Nonce":128,"Difficulty":2}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"Index":-1,"Timestamp":"2026-10-16T12:05:00.123456789+05:30","Transactions":[],"Nonce":11,"Difficulty":2}`))
	f.Add([]byte(`{"Transactions":null,"PrevHash":"é�"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var b domain.Block
		if err := json.Unmarshal(data, &b); err != nil {
			t.Skip()
		}

		hash := b.CalculateHash()
		if !lowerHex64.MatchString(hash) || b.CalculateHash() != hash {
			t.Fatalf("CalculateHash is not a stable lowercase 64-char hex: %q", hash)
		}

		encoded, err := json.Marshal(&b)
		if err != nil {
			t.Skip()
		}

		var decoded domain.Block
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("encoded block does not decode: %v", err)
		}

		if decoded.CalculateHash() != hash {
			t.Fatalf("round trip changed the hash: %s != %s", decoded.CalculateHash(), hash)
		}

		reencoded, err := json.Marshal(&decoded)
		if err != nil {
			t.Fatalf("decoded block does not encode: %v", err)
		}

		if string(reencoded) != string(encoded) {
			t.Fatalf("encoding is not idempotent:\n%s\n%s", encoded, reencoded)
		}
	})
}
