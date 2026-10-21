package chain_test

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

// allFailingRepo fails All once armed.
type allFailingRepo struct {
	storage.BlockRepository
	armed atomic.Bool
}

func (r *allFailingRepo) All() ([]*domain.Block, error) {
	if r.armed.Load() {
		return nil, errors.New("all failed")
	}
	return r.BlockRepository.All()
}

var _ = Describe("Service difficulty retargeting", func() {
	var repo *memory.Repository

	BeforeEach(func() { repo = memory.New() })

	It("records blocks at the difficulty the rule expects, raising it after a fast window", func() {
		eng := pow.New(1).WithRetarget(2, time.Hour)
		svc, f := funded(repo, eng)

		for i := 0; i < 4; i++ {
			_, err := svc.Mine(context.Background(), f.tx())
			Expect(err).NotTo(HaveOccurred())
		}

		all, _ := repo.All()
		Expect(all).To(HaveLen(5))
		var got []int
		for i := 1; i < len(all); i++ {
			got = append(got, all[i].Difficulty)
			Expect(eng.Verify(all[:i], all[i])).To(Succeed())
		}
		Expect(got).To(Equal([]int{1, 1, 1, 2}))
	})

	It("rejects a block sealed at another difficulty, leaving the repository and UTXO set untouched", func() {
		eng := pow.New(1).WithRetarget(2, time.Hour)
		svc, f := funded(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
			b, err := GenerateBlockWithContext(ctx, c, history, txs)
			Expect(err).NotTo(HaveOccurred())
			Expect(pow.MineWithContext(ctx, b, b.Difficulty+1)).To(Succeed())
			return b, nil
		}))
		before, utxos := svc.Balance(f.owner.Address())

		blk, err := svc.Mine(context.Background(), f.tx())
		Expect(err).To(MatchError(ErrInvalidBlock))
		Expect(blk).To(BeNil())

		all, _ := repo.All()
		Expect(all).To(HaveLen(1))
		after, utxosAfter := svc.Balance(f.owner.Address())
		Expect(after).To(Equal(before))
		Expect(utxosAfter).To(Equal(utxos))
	})

	It("passes the whole chain, genesis first, to the generator", func() {
		eng := pow.New(1)
		var seen []*domain.Block
		svc, f := funded(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
			seen = history
			return GenerateBlockWithContext(ctx, c, history, txs)
		}))
		_, err := svc.Mine(context.Background(), f.tx())
		Expect(err).NotTo(HaveOccurred())
		_, err = svc.Mine(context.Background(), f.tx())
		Expect(err).NotTo(HaveOccurred())

		Expect(seen).To(HaveLen(2))
		Expect(seen[0].Index).To(Equal(0))
		Expect(seen[1].Index).To(Equal(1))
	})

	It("returns the repository error when the history cannot be read", func() {
		failing := &allFailingRepo{BlockRepository: repo}
		svc, f := funded(failing, pow.New(1))
		failing.armed.Store(true)

		_, err := svc.Mine(context.Background(), f.tx())
		Expect(err).To(MatchError("all failed"))
	})
})
