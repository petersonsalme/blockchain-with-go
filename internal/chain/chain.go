package chain

import (
	"context"
	"strings"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

func GenerateBlockWithContext(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
	// The parent is the newest block; ranging avoids index arithmetic that a
	// slip would turn into a panic inside a Mine goroutine.
	var oldBlock *domain.Block
	for _, b := range history {
		oldBlock = b
	}

	var newBlock domain.Block

	newBlock.Index = oldBlock.Index + 1
	newBlock.Timestamp = time.Now()
	newBlock.Transactions = txs
	newBlock.MerkleRoot = domain.MerkleRoot(txs)
	newBlock.PrevHash = oldBlock.Hash

	// Check size before mining, using a full-length hash placeholder for a fair estimate.
	estimate := newBlock

	estimate.Hash = strings.Repeat("0", 64)

	err := domain.ValidateSize(&estimate, domain.MaxBlockBytes())
	if err != nil {
		return nil, err
	}

	err = c.Seal(ctx, history, &newBlock)
	if err != nil {
		return nil, err
	}

	return &newBlock, nil
}

func GenerateBlock(c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) *domain.Block {
	newBlock, _ := GenerateBlockWithContext(context.Background(), c, history, txs)
	return newBlock
}
