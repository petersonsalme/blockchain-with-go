package models

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Blockchain.replaceChain", func() {
	var (
		bc     *Blockchain
		a      *Block
		before []*Block
	)

	BeforeEach(func() {
		a = &Block{Index: 0}
		bc = &Blockchain{Blocks: []*Block{a}}
		before = bc.Blocks
	})

	It("keeps the current chain when the new one has the same length", func() {
		other := &Block{Index: 1}
		bc.replaceChain([]*Block{other})

		Expect(bc.Blocks).To(HaveLen(1))
		Expect(bc.Blocks[0]).To(BeIdenticalTo(a))
		Expect(bc.Blocks).To(Equal(before))
	})

	It("keeps the current chain when the new one is shorter", func() {
		bc.replaceChain(nil)

		Expect(bc.Blocks).To(HaveLen(1))
		Expect(bc.Blocks[0]).To(BeIdenticalTo(a))
	})

	It("replaces the chain when the new one is longer", func() {
		next := &Block{Index: 1}
		bc.replaceChain([]*Block{a, next})

		Expect(bc.Blocks).To(HaveLen(2))
		Expect(bc.Blocks[1]).To(BeIdenticalTo(next))
	})
})
