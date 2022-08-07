package models_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/go-blockchain/models"
)

var _ = Describe("Blockchain", func() {
	var b *Blockchain

	BeforeEach(func() {
		b = &Blockchain{
			Blocks: make([]*Block, 0),
		}
	})

	Describe("LastBlock", func() {
		It("should return the last block added successfully", func() {
			newBlock := &Block{}
			b.Blocks = append(b.Blocks, newBlock)

			Expect(b.LastBlock()).To(BeIdenticalTo(newBlock))
		})
	})
	Describe("Add", func() {
		It("should add a new block at the end of the chain successfully", func() {
			newBlock := &Block{}
			b.Add(newBlock)

			Expect(b.Blocks[0]).To(BeIdenticalTo(newBlock))
		})
	})
	Describe("Dump", func() {
		It("shouldn't throw any error", func() {
			b.Dump()
		})
	})
})
