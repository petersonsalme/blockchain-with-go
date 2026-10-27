package chain

import (
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// genesisOf opens a Service over an empty repository and returns its block 0.
func genesisOf(t *testing.T, opts ...Option) *domain.Block {
	t.Helper()

	svc, err := NewService(memory.New(), pow.NewProofOfWork(1), opts...)
	if err != nil {
		t.Fatal(err)
	}

	b0, err := svc.BlockByHeight(0)
	if err != nil {
		t.Fatal(err)
	}

	return b0
}

func TestGenesisTimeMakesGenesisDeterministic(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	owner, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	outs := []domain.TxOut{{Amount: 7, Address: owner.Address()}}

	a := genesisOf(t, WithGenesisTime(t0), WithGenesisAllocations(outs))
	b := genesisOf(t, WithGenesisTime(t0), WithGenesisAllocations(outs))

	for name, b0 := range map[string]*domain.Block{"first": a, "second": b} {
		if !b0.Timestamp.Equal(t0) {
			t.Errorf("%s genesis timestamp = %v, want %v", name, b0.Timestamp, t0)
		}

		if !b0.Transactions[0].Timestamp.Equal(t0) {
			t.Errorf("%s allocation timestamp = %v, want %v", name, b0.Transactions[0].Timestamp, t0)
		}
	}

	if a.CalculateHash() != b.CalculateHash() {
		t.Errorf("genesis hashes differ: %s != %s", a.CalculateHash(), b.CalculateHash())
	}

	if a.Transactions[0].ID != b.Transactions[0].ID {
		t.Errorf("allocation IDs differ: %s != %s", a.Transactions[0].ID, b.Transactions[0].ID)
	}
}

func TestZeroGenesisTimeUsesTheClock(t *testing.T) {
	owner, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	outs := []domain.TxOut{{Amount: 7, Address: owner.Address()}}

	cases := map[string][]Option{
		"sem opção":                    {WithGenesisAllocations(outs)},
		"WithGenesisTime(time.Time{})": {WithGenesisTime(time.Time{}), WithGenesisAllocations(outs)},
	}

	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			before := time.Now()
			b0 := genesisOf(t, opts...)
			after := time.Now()

			if b0.Timestamp.Before(before) || b0.Timestamp.After(after) {
				t.Errorf("genesis timestamp %v outside [%v, %v]", b0.Timestamp, before, after)
			}
		})
	}
}

func TestGenesisTimeIgnoredOnPopulatedRepository(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	repo := memory.New()

	first, err := NewService(repo, pow.NewProofOfWork(1))
	if err != nil {
		t.Fatal(err)
	}

	before, err := first.BlockByHeight(0)
	if err != nil {
		t.Fatal(err)
	}

	second, err := NewService(repo, pow.NewProofOfWork(1), WithGenesisTime(t0))
	if err != nil {
		t.Fatal(err)
	}

	after, err := second.BlockByHeight(0)
	if err != nil {
		t.Fatal(err)
	}

	if after.CalculateHash() != before.CalculateHash() {
		t.Errorf("genesis hash changed on a populated repository")
	}

	if !after.Timestamp.Equal(before.Timestamp) {
		t.Errorf("genesis timestamp changed on a populated repository: %v != %v", after.Timestamp, before.Timestamp)
	}
}
