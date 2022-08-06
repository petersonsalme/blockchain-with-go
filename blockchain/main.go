package blockchain

import (
	"time"

	"github.com/petersonsalme/go-blockchain/models"
)

func New() *models.Blockchain {
	genesisBlock := models.Block{
		Index:     0,
		Timestamp: time.Now().String(),
		BPM:       0,
		Hash:      "",
		PrevHash:  "",
	}

	b := models.Blockchain{}
	b.Add(genesisBlock)

	return &b
}

func GenerateBlock(oldBlock *models.Block, BPM int) (models.Block, error) {
	var newBlock models.Block

	t := time.Now()

	newBlock.Index = oldBlock.Index + 1
	newBlock.Timestamp = t.String()
	newBlock.BPM = BPM
	newBlock.PrevHash = oldBlock.Hash
	newBlock.Hash = newBlock.CalculateHash()

	return newBlock, nil
}
