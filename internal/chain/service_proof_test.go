package chain_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

var _ = Describe("Service.TxProof", func() {
	var (
		repo *memory.Repository
		svc  *Service
		f    *funding
	)

	mineBlock := func(n int) {
		var txs []domain.Transaction
		for i := 0; i < n; i++ {
			txs = append(txs, f.tx()...)
		}
		_, err := svc.Mine(context.Background(), txs)
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeEach(func() {
		repo = memory.New()
		svc, f = funded(repo, pow.New(1))
		for _, n := range []int{1, 2, 3, 5} {
			mineBlock(n)
		}
	})

	It("proves every transaction of the chain against the Merkle root of its block", func() {
		blocks, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(blocks).To(HaveLen(5))

		count := 0
		for _, b := range blocks {
			for i := range b.Transactions {
				tx := &b.Transactions[i]
				p, err := svc.TxProof(tx.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(p.BlockIndex).To(Equal(b.Index))
				Expect(p.BlockHash).To(Equal(b.Hash))
				Expect(p.MerkleRoot).To(Equal(b.MerkleRoot))
				Expect(p.LeafIndex).To(Equal(i))
				Expect(p.LeafHash).To(Equal(domain.MerkleLeafHash(tx)))
				Expect(domain.VerifyMerkleProof(p.LeafHash, p.Steps, p.MerkleRoot)).To(BeTrue())
				count++
			}
		}
		Expect(count).To(Equal(1 + 1 + 2 + 3 + 5))
	})

	It("gives an empty proof for the only transaction of a block", func() {
		blocks, _ := repo.All()
		p, err := svc.TxProof(blocks[1].Transactions[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Steps).To(BeEmpty())
		Expect(p.LeafHash).To(Equal(p.MerkleRoot))
	})

	It("finds the genesis allocation at index 0", func() {
		blocks, _ := repo.All()
		p, err := svc.TxProof(blocks[0].Transactions[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.BlockIndex).To(Equal(0))
		Expect(p.LeafIndex).To(Equal(0))
	})

	It("returns ErrTxNotFound for an unknown id", func() {
		p, err := svc.TxProof("deadbeef")
		Expect(err).To(MatchError(ErrTxNotFound))
		Expect(p).To(BeNil())
	})

	It("leaves the repository and the UTXO set untouched", func() {
		blocks, _ := repo.All()
		before, utxos := svc.Balance(f.owner.Address())

		_, err := svc.TxProof(blocks[3].Transactions[1].ID)
		Expect(err).NotTo(HaveOccurred())
		_, err = svc.TxProof("deadbeef")
		Expect(err).To(MatchError(ErrTxNotFound))

		after, _ := repo.All()
		Expect(after).To(Equal(blocks))
		balance, utxosAfter := svc.Balance(f.owner.Address())
		Expect(balance).To(Equal(before))
		Expect(utxosAfter).To(Equal(utxos))
	})

	It("returns the repository error, not ErrTxNotFound, when the chain cannot be read", func() {
		boom := errors.New("boom")
		failing := &failingRepo{IndexedRepository: memory.New()}
		s, _ := funded(failing, pow.New(1))
		failing.txErr = boom

		_, err := s.TxProof("deadbeef")
		Expect(err).To(MatchError(boom))
		Expect(errors.Is(err, ErrTxNotFound)).To(BeFalse())
	})

	It("does not wait for a Mine in progress", func() {
		entered := make(chan struct{})
		release := make(chan struct{})
		stuck, sf := funded(memory.New(), pow.New(1), WithGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, ctx.Err()
		}))
		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			_, _ = stuck.Mine(context.Background(), sf.tx())
		}()
		Eventually(entered).Should(BeClosed())
		defer func() { close(release); <-done }()

		all, _ := stuck.Blocks()
		start := time.Now()
		_, err := stuck.TxProof(all[0].Transactions[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(time.Since(start)).To(BeNumerically("<", time.Second))
	})
})
