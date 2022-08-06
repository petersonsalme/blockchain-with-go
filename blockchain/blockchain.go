package blockchain

import (
	"time"

	"github.com/petersonsalme/go-blockchain/models"
)

func New() *models.Blockchain {
	genesisBlock := models.Block{
		Index:     0,
		Timestamp: time.Now(),
		BPM:       0,
		Hash:      "",
		PrevHash:  "",
	}

	b := models.Blockchain{}
	b.Add(&genesisBlock)

	return &b
}

func GenerateBlock(oldBlock *models.Block, BPM int) *models.Block {
	var newBlock models.Block

	newBlock.Index = oldBlock.Index + 1
	newBlock.Timestamp = time.Now()
	newBlock.BPM = BPM
	newBlock.PrevHash = oldBlock.Hash
	newBlock.Hash = newBlock.CalculateHash()

	return &newBlock
}
