package chain_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var _ = Describe("Service transaction verification", func() {
	var (
		repo *memory.Repository
		eng  *pow.ProofOfWork
		svc  *Service
		alice,
		bob *wallet.Wallet
	)

	mine := func(txs ...domain.Transaction) error {
		_, err := svc.Mine(context.Background(), txs)
		return err
	}
	chainLen := func() int {
		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		return len(all)
	}

	BeforeEach(func() {
		var err error
		repo = memory.New()
		eng = pow.NewProofOfWork(1)
		svc, err = NewService(repo, eng)
		Expect(err).NotTo(HaveOccurred())
		alice, err = wallet.New()
		Expect(err).NotTo(HaveOccurred())
		bob, err = wallet.New()
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("rejects a bad signature without changing the chain",
		func(mutate func(tx *domain.Transaction), want error) {
			tx := signedTx(alice, 0)
			mutate(&tx)
			Expect(mine(tx)).To(MatchError(want))
			Expect(chainLen()).To(Equal(1))
			// the nonce was not consumed
			Expect(mine(signedTx(alice, 0))).To(Succeed())
		},
		Entry("unsigned", func(tx *domain.Transaction) { tx.Signature = "" }, domain.ErrMissingSignature),
		Entry("tampered amount", func(tx *domain.Transaction) { tx.Amount++ }, domain.ErrInvalidSignature),
		Entry("bad public key", func(tx *domain.Transaction) { tx.PublicKey = "zz" }, domain.ErrInvalidPublicKey),
		Entry("bad recipient", func(tx *domain.Transaction) { tx.To = "nope" }, domain.ErrInvalidRecipient),
		Entry("sender mismatch", func(tx *domain.Transaction) { tx.From = bob.Address() }, domain.ErrSenderMismatch),
	)

	It("starts every account at nonce 0 and advances by one per accepted transaction", func() {
		Expect(mine(signedTx(alice, 0))).To(Succeed())
		Expect(mine(signedTx(alice, 1))).To(Succeed())
		Expect(mine(signedTx(bob, 0))).To(Succeed())
		Expect(chainLen()).To(Equal(4))
	})

	It("rejects a lower nonce as nonce_reused", func() {
		Expect(mine(signedTx(alice, 0))).To(Succeed())
		Expect(mine(signedTx(alice, 1))).To(Succeed())
		err := mine(signedTx(alice, 0))
		Expect(err).To(MatchError(domain.ErrNonceReused))
		Expect(chainLen()).To(Equal(3))
	})

	It("rejects a higher nonce as nonce_gap", func() {
		err := mine(signedTx(alice, 1))
		Expect(err).To(MatchError(domain.ErrNonceGap))
		Expect(chainLen()).To(Equal(1))
		Expect(mine(signedTx(alice, 0))).To(Succeed())
	})

	It("accepts consecutive nonces from one sender in one request", func() {
		Expect(mine(signedTx(alice, 0), signedTx(alice, 1), signedTx(bob, 0))).To(Succeed())
		Expect(mine(signedTx(alice, 2))).To(Succeed())
	})

	It("rejects a gap inside a request and leaves every nonce untouched", func() {
		err := mine(signedTx(bob, 0), signedTx(alice, 0), signedTx(alice, 2))
		Expect(err).To(MatchError(domain.ErrNonceGap))
		Expect(chainLen()).To(Equal(1))
		Expect(mine(signedTx(bob, 0), signedTx(alice, 0))).To(Succeed())
	})

	It("rejects a repeated nonce inside a request as nonce_reused", func() {
		err := mine(signedTx(alice, 0), signedTx(alice, 0))
		Expect(err).To(MatchError(domain.ErrNonceReused))
	})

	It("rejects the same transaction repeated in a request as duplicate_transaction", func() {
		tx := signedTx(alice, 0)
		Expect(mine(tx, tx)).To(MatchError(domain.ErrDuplicateTransaction))
		Expect(chainLen()).To(Equal(1))
	})

	It("detects duplicates by hash even when the ID field is blanked", func() {
		tx := signedTx(alice, 0)
		blank := tx
		blank.ID = ""
		Expect(mine(tx, blank)).To(MatchError(domain.ErrDuplicateTransaction))
	})

	It("rejects a byte-for-byte replay of an accepted transaction", func() {
		tx := signedTx(alice, 0)
		Expect(mine(tx)).To(Succeed())
		Expect(mine(tx)).To(MatchError(domain.ErrNonceReused))
		Expect(chainLen()).To(Equal(2))
	})

	It("does not advance the nonce when Append fails", func() {
		boom := errors.New("boom")
		fr := &failingRepo{BlockRepository: repo}
		s, err := NewService(fr, eng)
		Expect(err).NotTo(HaveOccurred())
		fr.appendErr = boom
		_, err = s.Mine(context.Background(), []domain.Transaction{signedTx(alice, 0)})
		Expect(err).To(MatchError(boom))
		fr.appendErr = nil
		_, err = s.Mine(context.Background(), []domain.Transaction{signedTx(alice, 0)})
		Expect(err).NotTo(HaveOccurred())
	})

	It("does not advance the nonce when the generator fails", func() {
		var fail atomic.Bool
		fail.Store(true)
		s, err := NewService(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
			if fail.Load() {
				return nil, errors.New("boom")
			}
			return GenerateBlockWithContext(ctx, c, prev, txs)
		}))
		Expect(err).NotTo(HaveOccurred())
		_, err = s.Mine(context.Background(), []domain.Transaction{signedTx(alice, 0)})
		Expect(err).To(HaveOccurred())
		fail.Store(false)
		_, err = s.Mine(context.Background(), []domain.Transaction{signedTx(alice, 0)})
		Expect(err).NotTo(HaveOccurred())
	})

	It("rebuilds the expected nonces from the repository", func() {
		Expect(mine(signedTx(alice, 0), signedTx(alice, 1))).To(Succeed())
		Expect(mine(signedTx(bob, 0))).To(Succeed())

		rebuilt, err := NewService(repo, eng)
		Expect(err).NotTo(HaveOccurred())

		_, err = rebuilt.Mine(context.Background(), []domain.Transaction{signedTx(alice, 1)})
		Expect(err).To(MatchError(domain.ErrNonceReused))
		_, err = rebuilt.Mine(context.Background(), []domain.Transaction{signedTx(alice, 3)})
		Expect(err).To(MatchError(domain.ErrNonceGap))
		_, err = rebuilt.Mine(context.Background(), []domain.Transaction{signedTx(bob, 0)})
		Expect(err).To(MatchError(domain.ErrNonceReused))
		_, err = rebuilt.Mine(context.Background(), []domain.Transaction{signedTx(alice, 2), signedTx(bob, 1)})
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts exactly one of 50 concurrent submissions of the same signed transaction", func() {
		tx := signedTx(alice, 0)
		const n = 50
		var wg sync.WaitGroup
		var ok, reused atomic.Int32
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				switch err := mine(tx); {
				case err == nil:
					ok.Add(1)
				case errors.Is(err, domain.ErrNonceReused):
					reused.Add(1)
				}
			}()
		}
		wg.Wait()
		Expect(ok.Load()).To(Equal(int32(1)))
		Expect(reused.Load()).To(Equal(int32(n - 1)))
		Expect(chainLen()).To(Equal(2))
	})
})
