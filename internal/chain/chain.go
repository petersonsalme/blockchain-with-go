package chain

import (
	"context"
	"strings"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

func GenerateBlockWithContext(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
	oldBlock := history[len(history)-1]
	var newBlock domain.Block

	newBlock.Index = oldBlock.Index + 1
	newBlock.Timestamp = time.Now()
	newBlock.Transactions = txs
	newBlock.PrevHash = oldBlock.Hash

	// Check size before mining, using a full-length hash placeholder for a fair estimate.
	estimate := newBlock
	estimate.Hash = strings.Repeat("0", 64)
	if err := domain.ValidateSize(&estimate, domain.MaxBlockBytes()); err != nil {
		return nil, err
	}

	if err := c.Seal(ctx, history, &newBlock); err != nil {
		return nil, err
	}

	return &newBlock, nil
}

func GenerateBlock(c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) *domain.Block {
	newBlock, _ := GenerateBlockWithContext(context.Background(), c, history, txs)
	return newBlock
}
