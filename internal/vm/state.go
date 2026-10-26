package vm

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// State errors.
var (
	ErrContractExists  = errors.New("vm: contract already exists")
	ErrUnknownContract = errors.New("vm: unknown contract")
)

// Contract is a deployed contract: its code and its storage. Absent storage
// keys read as 0. Callers must treat a Contract obtained from a State as
// read-only.
type Contract struct {
	Code    []byte
	Storage map[uint64]uint64
}

// StateView is the read-only access BlockDelta needs.
type StateView interface {
	Contract(id string) (Contract, bool)
}

// State maps a contract ID (the hash of its deploying transaction) to the
// contract. It is derived from the chain by BuildState and never persisted.
type State map[string]Contract

// Contract returns the contract with the given ID.
func (s State) Contract(id string) (Contract, bool) {
	c, ok := s[id]
	return c, ok
}

// Delta is the effect of one block on the contract state.
type Delta struct {
	// Deploys holds the code of the contracts the block creates.
	Deploys map[string][]byte
	// Writes holds the storage writes of the block's calls, by contract ID.
	Writes map[string]map[uint64]uint64
}

// Apply adds d to s. d must come from BlockDelta over s.
func (s *State) Apply(d Delta) {
	if *s == nil {
		*s = State{}
	}

	for id, code := range d.Deploys {
		(*s)[id] = Contract{Code: code, Storage: map[uint64]uint64{}}
	}

	for id, writes := range d.Writes {
		c := (*s)[id]
		for k, v := range writes {
			c.Storage[k] = v
		}
	}
}

// BuildState replays blocks from empty state. It fails on the first block
// whose contract operations fail.
func BuildState(blocks []*domain.Block) (State, error) {
	s := State{}
	for i, b := range blocks {
		d, err := BlockDelta(s, b)
		if err != nil {
			return nil, fmt.Errorf("block %d: %w", i, err)
		}

		s.Apply(d)
	}

	return s, nil
}

// BlockDelta computes the effect of b's contract operations over view, in
// block order: a call sees the writes of the block's earlier transactions.
// Any failing operation invalidates the block and the Delta is empty. It
// never alters view.
func BlockDelta(view StateView, b *domain.Block) (Delta, error) {
	var d Delta

	for i := range b.Transactions {
		tx := &b.Transactions[i]
		if err := domain.ValidateContractOp(tx.Contract); err != nil {
			return Delta{}, fmt.Errorf("transaction %d: %w", i, err)
		}

		if tx.Contract == nil {
			continue
		}

		var err error
		if tx.Contract.Kind == domain.ContractDeploy {
			err = d.deploy(view, tx)
		} else {
			err = d.call(view, tx.Contract)
		}

		if err != nil {
			return Delta{}, fmt.Errorf("transaction %d: %w", i, err)
		}
	}

	return d, nil
}

// lookup finds a contract among d's deploys, then in view. base is the
// storage the contract had before the block.
func (d *Delta) lookup(view StateView, id string) (code []byte, base map[uint64]uint64, ok bool) {
	if code, ok = d.Deploys[id]; ok {
		return code, nil, true
	}

	c, ok := view.Contract(id)

	return c.Code, c.Storage, ok
}

func (d *Delta) deploy(view StateView, tx *domain.Transaction) error {
	id := tx.CalculateHash()

	code, _ := hex.DecodeString(tx.Contract.Code)
	if err := ValidateCode(code); err != nil {
		return err
	}

	if _, _, exists := d.lookup(view, id); exists {
		return ErrContractExists
	}

	if d.Deploys == nil {
		d.Deploys = map[string][]byte{}
	}

	d.Deploys[id] = code

	return nil
}

func (d *Delta) call(view StateView, op *domain.ContractOp) error {
	code, base, ok := d.lookup(view, op.Target)
	if !ok {
		return ErrUnknownContract
	}

	res, err := Execute(code, op.Args, layered{base: base, writes: d.Writes[op.Target]}, op.GasLimit)
	if err != nil {
		return err
	}

	if d.Writes == nil {
		d.Writes = map[string]map[uint64]uint64{}
	}

	if d.Writes[op.Target] == nil {
		d.Writes[op.Target] = map[uint64]uint64{}
	}

	for k, v := range res.Writes {
		d.Writes[op.Target][k] = v
	}

	return nil
}

// layered is a contract's storage with the block's earlier writes on top.
type layered struct {
	base, writes map[uint64]uint64
}

func (l layered) Load(key uint64) uint64 {
	if v, ok := l.writes[key]; ok {
		return v
	}

	return l.base[key]
}
