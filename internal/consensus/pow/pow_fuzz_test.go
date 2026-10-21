package pow_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	powpkg "github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// cloneBlock copies b deeply enough that tampering the copy leaves b intact.
func cloneBlock(b domain.Block) domain.Block {
	txs := make([]domain.Transaction, len(b.Transactions))
	for i, tx := range b.Transactions {
		tx.Inputs = append([]domain.TxIn(nil), tx.Inputs...)
		tx.Outputs = append([]domain.TxOut(nil), tx.Outputs...)
		txs[i] = tx
	}
	b.Transactions = txs
	return b
}

// tamper changes exactly one field of the hash preimage chosen by sel and
// returns the error Verify must report. A selector that needs a transaction
// part the block lacks falls back to the nonce.
func tamper(b *domain.Block, sel uint8) error {
	var txs []*domain.Transaction
	for i := range b.Transactions {
		txs = append(txs, &b.Transactions[i])
	}
	tx := func(pick uint8) *domain.Transaction { return txs[int(pick)%len(txs)] }

	switch sel % 13 {
	case 0:
		b.Index++
		return consensus.ErrInvalidIndex
	case 1:
		b.Timestamp = b.Timestamp.Add(time.Nanosecond)
	case 2:
		b.PrevHash += "x"
		return consensus.ErrInvalidPrevHash
	case 3:
		b.Nonce++
	case 4:
		b.Difficulty++
	case 5:
		b.Hash += "x"
	case 6:
		if len(txs) > 0 {
			t := tx(sel / 13)
			t.Timestamp = t.Timestamp.Add(time.Nanosecond)
			return consensus.ErrInvalidHash
		}
	case 7:
		if len(txs) > 0 && len(tx(sel/13).Inputs) > 0 {
			t := tx(sel / 13)
			t.Inputs[int(sel/13)%len(t.Inputs)].PrevTxID += "x"
			return consensus.ErrInvalidHash
		}
	case 8:
		if len(txs) > 0 && len(tx(sel/13).Inputs) > 0 {
			t := tx(sel / 13)
			t.Inputs[int(sel/13)%len(t.Inputs)].OutIndex++
			return consensus.ErrInvalidHash
		}
	case 9:
		if len(txs) > 0 && len(tx(sel/13).Inputs) > 0 {
			t := tx(sel / 13)
			t.Inputs[int(sel/13)%len(t.Inputs)].Signature += "x"
			return consensus.ErrInvalidHash
		}
	case 10:
		if len(txs) > 0 && len(tx(sel/13).Outputs) > 0 {
			t := tx(sel / 13)
			t.Outputs[int(sel/13)%len(t.Outputs)].Amount++
			return consensus.ErrInvalidHash
		}
	case 11:
		if len(txs) > 0 && len(tx(sel/13).Outputs) > 0 {
			t := tx(sel / 13)
			t.Outputs[int(sel/13)%len(t.Outputs)].Address += "x"
			return consensus.ErrInvalidHash
		}
	case 12:
		// A consistent hash carrying an (unreachable) difficulty the rule never expects.
		b.Difficulty = 65
		b.Hash = b.CalculateHash()
		return consensus.ErrInvalidDifficulty
	}

	if sel%13 >= 6 && sel%13 <= 11 {
		b.Nonce++
	}
	return consensus.ErrInvalidHash
}

func FuzzVerifyTamper(f *testing.F) {
	f.Add(uint8(3), int64(1_700_000_000), int64(5), "prev", uint8(0), uint8(2), uint8(1), uint8(1), "s", uint64(10), uint32(0), uint8(0))
	f.Add(uint8(0), int64(1_700_000_000), int64(0), "", uint8(1), uint8(1), uint8(2), uint8(2), "addr", uint64(1), uint32(1), uint8(9))
	f.Add(uint8(7), int64(1_600_000_000), int64(999), "00ab", uint8(2), uint8(0), uint8(0), uint8(0), "", uint64(0), uint32(0), uint8(4))

	for sel := uint8(0); sel < 13; sel++ {
		f.Add(uint8(1), int64(1_700_000_000), int64(1), "p", uint8(sel%3), uint8(2), uint8(2), uint8(2), "z", uint64(5), uint32(3), sel)
	}

	f.Fuzz(func(t *testing.T, index uint8, sec, nsec int64, prevHash string, diff, nTx, nIn, nOut uint8, s string, amount uint64, outIndex uint32, sel uint8) {
		ts := time.Unix(sec%(1<<35), nsec%1_000_000_000).UTC()
		prev := &domain.Block{Index: int(index), Hash: prevHash}
		b := domain.Block{Index: int(index) + 1, Timestamp: ts, PrevHash: prevHash}
		for i := 0; i < int(nTx%3); i++ {
			tx := domain.Transaction{Timestamp: ts.Add(time.Duration(i))}
			for j := 0; j < int(nIn%3); j++ {
				tx.Inputs = append(tx.Inputs, domain.TxIn{PrevTxID: s + strings.Repeat("p", i), OutIndex: outIndex + uint32(j), Signature: s + strings.Repeat("g", j)})
			}
			for j := 0; j < int(nOut%3); j++ {
				tx.Outputs = append(tx.Outputs, domain.TxOut{Amount: amount + uint64(j), Address: s})
			}
			b.Transactions = append(b.Transactions, tx)
		}

		if err := powpkg.MineWithContext(context.Background(), &b, int(diff%3)); err != nil {
			t.Fatal(err)
		}
		p := powpkg.New(int(diff % 3))
		if err := p.Verify([]*domain.Block{prev}, &b); err != nil {
			t.Fatalf("mined block rejected: %v", err)
		}

		tampered := cloneBlock(b)
		want := tamper(&tampered, sel)
		if err := p.Verify([]*domain.Block{prev}, &tampered); !errors.Is(err, want) {
			t.Fatalf("tamper %d: Verify = %v, want %v", sel%13, err, want)
		}
	})
}
