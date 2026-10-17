package blockchain

import (
	"context"
	"time"

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

func GenerateBlockWithContext(ctx context.Context, oldBlock *models.Block, txs []models.Transaction) (*models.Block, error) {
	var newBlock models.Block

	newBlock.Index = oldBlock.Index + 1
	newBlock.Timestamp = time.Now()
	newBlock.Transactions = txs
	newBlock.PrevHash = oldBlock.Hash
	if err := newBlock.MineWithContext(ctx, models.Difficulty()); err != nil {
		return nil, err
	}

	return &newBlock, nil
}

func GenerateBlock(oldBlock *models.Block, txs []models.Transaction) *models.Block {
	newBlock, _ := GenerateBlockWithContext(context.Background(), oldBlock, txs)
	return newBlock
}
