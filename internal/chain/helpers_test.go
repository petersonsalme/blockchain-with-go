package chain_test

import (
	"time"

	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

const fundedOutputs = 12

// funding hands out one signed, valid spend of a genesis allocation per call.
type funding struct {
	owner *wallet.Wallet
	ops   []domain.OutPoint
	next  int
}

// funded builds a Service whose genesis allocates fundedOutputs outputs of 10 to a fresh owner.
func funded(repo storage.BlockRepository, c consensus.Consensus, opts ...Option) (*Service, *funding) {
	owner, err := wallet.New()
	Expect(err).NotTo(HaveOccurred())

	allocs := make([]domain.TxOut, fundedOutputs)
	for i := range allocs {
		allocs[i] = domain.TxOut{Amount: 10, Address: owner.Address()}
	}

	svc, err := NewService(repo, c, append([]Option{WithGenesisAllocations(allocs)}, opts...)...)
	Expect(err).NotTo(HaveOccurred())

	f := &funding{owner: owner}
	blocks, err := repo.All()
	if err != nil || len(blocks[0].Transactions) == 0 {
		return svc, f
	}
	txid := blocks[0].Transactions[0].CalculateHash()
	for i := range allocs {
		f.ops = append(f.ops, domain.OutPoint{TxID: txid, Index: uint32(i)})
	}
	return svc, f
}

// tx spends the next unused genesis allocation.
func (f *funding) tx() []domain.Transaction {
	op := f.ops[f.next]
	f.next++
	return []domain.Transaction{spend(f.owner, op, 9)}
}

// spend builds a transaction that spends op with w and pays out to a fixed recipient.
func spend(w *wallet.Wallet, op domain.OutPoint, out uint64) domain.Transaction {
	tx := domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: op.TxID, OutIndex: op.Index}},
		Outputs:   []domain.TxOut{{Amount: out, Address: recipient().Address()}},
		Timestamp: time.Now(),
	}
	tx.ID = tx.CalculateHash()
	Expect(domain.SignInput(&tx, 0, w)).To(Succeed())
	return tx
}

func recipient() *wallet.Wallet {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = 7
	}
	w, err := wallet.FromSeed(seed)
	Expect(err).NotTo(HaveOccurred())
	return w
}
