package blockchain

import (
	"context"
	"strings"
	"time"

	"github.com/petersonsalme/blockchain-with-go/consensus"
	"github.com/petersonsalme/blockchain-with-go/models"
)

func New() *models.Blockchain {
	genesisBlock := models.Block{
		Index:        0,
		Timestamp:    time.Now(),
		Transactions: []models.Transaction{},
		Hash:         "",
		PrevHash:     "",
	}

	b := models.Blockchain{}
	b.Add(&genesisBlock)

	return &b
}

func GenerateBlockWithContext(ctx context.Context, c consensus.Consensus, oldBlock *models.Block, txs []models.Transaction) (*models.Block, error) {
	var newBlock models.Block

	newBlock.Index = oldBlock.Index + 1
	newBlock.Timestamp = time.Now()
	newBlock.Transactions = txs
	newBlock.PrevHash = oldBlock.Hash

	// Check size before mining, using a full-length hash placeholder for a fair estimate.
	estimate := newBlock
	estimate.Hash = strings.Repeat("0", 64)
	if err := models.ValidateSize(&estimate, models.MaxBlockBytes()); err != nil {
		return nil, err
	}

	if err := c.Seal(ctx, &newBlock); err != nil {
		return nil, err
	}

	return &newBlock, nil
}

func GenerateBlock(c consensus.Consensus, oldBlock *models.Block, txs []models.Transaction) *models.Block {
	newBlock, _ := GenerateBlockWithContext(context.Background(), c, oldBlock, txs)
	return newBlock
}
