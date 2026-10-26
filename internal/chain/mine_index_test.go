package chain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// tipRepo serves a fixed history and an empty UTXO set.
type tipRepo struct {
	storage.IndexedRepository

	blocks []*domain.Block
}

func (r tipRepo) All() ([]*domain.Block, error)             { return r.blocks, nil }
func (tipRepo) Lookup(domain.OutPoint) (domain.TxOut, bool) { return domain.TxOut{}, false }

// TestMineLockedValidatesWithTheNextIndex pins that mineLocked checks the block
// against Subsidy(prev.Index+1): the Service creates the coinbase in a later
// story, so the Index is not reachable through the public API yet.
func TestMineLockedValidatesWithTheNextIndex(t *testing.T) {
	errSealed := errors.New("reached the generator")
	addr := wallet.AddressFromPublicKey(make([]byte, 32))
	coinbase := func(amount uint64) []domain.Transaction {
		return []domain.Transaction{{Outputs: []domain.TxOut{{Amount: amount, Address: addr}}, Timestamp: time.Now()}}
	}
	s := &Service{
		repo: tipRepo{blocks: []*domain.Block{{Index: domain.HalvingInterval - 1}}},
		generate: func(context.Context, consensus.Consensus, []*domain.Block, []domain.Transaction) (*domain.Block, error) {
			return nil, errSealed
		},
	}

	half := domain.InitialSubsidy / 2
	if _, err := s.mineLocked(context.Background(), coinbase(half)); !errors.Is(err, errSealed) {
		t.Fatalf("coinbase at the halved subsidy: got %v, want to reach the generator", err)
	}

	if _, err := s.mineLocked(context.Background(), coinbase(half+1)); !errors.Is(err, domain.ErrCoinbaseTooLarge) {
		t.Fatalf("coinbase above the halved subsidy: got %v, want ErrCoinbaseTooLarge", err)
	}
}
