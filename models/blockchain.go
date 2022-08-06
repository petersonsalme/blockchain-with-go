package models

import "github.com/davecgh/go-spew/spew"

type Blockchain struct {
	Blocks []*Block
}

func (b *Blockchain) LastBlock() *Block {
	index := len(b.Blocks) - 1
	return b.Blocks[index]
}

func (b *Blockchain) Add(block *Block) {
	newBlockchain := append(b.Blocks, block)
	b.replaceChain(newBlockchain)
}

func (b *Blockchain) Dump() {
	spew.Sdump(b.Blocks)
}

func (b *Blockchain) replaceChain(newBlocks []*Block) {
	if len(newBlocks) > len(b.Blocks) {
		b.Blocks = newBlocks
	}
}
