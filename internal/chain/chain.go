package chain

import (
	"context"
	"errors"
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
	// An honest miner whose clock is behind the median still produces a valid block (ADR-0034).
	newBlock.Timestamp = time.Now()

	if median := domain.MedianTimePast(history).Add(time.Nanosecond); newBlock.Timestamp.Before(median) {
		newBlock.Timestamp = median
	}

	newBlock.Transactions = txs
	newBlock.MerkleRoot = domain.MerkleRoot(txs)
	newBlock.PrevHash = oldBlock.Hash

	// Check size before mining, using a full-length hash placeholder for a fair estimate.
	estimate := newBlock

	estimate.Hash = strings.Repeat("0", 64)

	err := domain.ValidateSize(&estimate, domain.MaxBlockBytes)
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

// ErrInvalidStoredChain is wrapped by Verify around the rule error of the first
// stored block that fails the current block rules (ADR-0035).
var ErrInvalidStoredChain = errors.New("chain: invalid stored chain")
