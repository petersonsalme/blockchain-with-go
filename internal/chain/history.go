package chain

import (
	"errors"
	"slices"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// AddressTx is one confirmed transaction of an address history and its block.
type AddressTx struct {
	Tx        domain.Transaction
	BlockHash string
	Height    int
	Timestamp time.Time
}

// AddressPage is one page of an address history, newest first.
type AddressPage struct {
	Txs         []AddressTx // never nil
	More        bool        // older transactions remain after the last item
	PrunedBelow int         // heights below it are not indexed (0: nothing pruned)
}

// ErrUnknownCursor is returned by AddressHistory when after is not in the address history.
var ErrUnknownCursor = errors.New("chain: cursor is not in the address history")

// AddressHistory returns up to limit (callers pass 1..100) transactions of addr,
// newest first, that come strictly after the transaction after; "" starts at the newest.
// It reads through consistent and never waits for Mine.
func (s *Service) AddressHistory(addr, after string, limit int) (AddressPage, error) {
	var page AddressPage

	err := s.consistent(func() error {
		ids, err := s.repo.TxsByAddress(addr)
		if err != nil {
			return err
		}

		slices.Reverse(ids)

		start := 0

		if after != "" {
			i := slices.Index(ids, after)
			if i < 0 {
				return ErrUnknownCursor
			}

			start = i + 1
		}

		end := min(start+limit, len(ids))

		txs, err := s.locateAll(ids[start:end])
		if err != nil {
			return err
		}

		below, err := s.repo.PrunedBelow()
		if err != nil {
			return err
		}

		page = AddressPage{Txs: txs, More: end < len(ids), PrunedBelow: below}

		return nil
	})
	if err != nil {
		return AddressPage{Txs: []AddressTx{}}, err
	}

	return page, nil
}
