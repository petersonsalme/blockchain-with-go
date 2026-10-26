package main

import (
	"errors"
	"fmt"
	"math/bits"
	"time"

	"github.com/petersonsalme/blockchain-with-go/client"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var (
	errZeroAmount        = errors.New("amount must be at least 1")
	errInsufficientFunds = errors.New("insufficient funds")
	errAmountOverflow    = errors.New("amount overflows uint64")
)

// buildTransfer spends utxos of w, in order, until they cover amount+fee, pays
// amount to to, returns any change to w and signs every input.
func buildTransfer(w *wallet.Wallet, utxos []client.Utxo, to string, amount, fee uint64, now time.Time) (domain.Transaction, error) {
	if amount == 0 {
		return domain.Transaction{}, errZeroAmount
	}

	err := wallet.ValidateAddress(to)
	if err != nil {
		return domain.Transaction{}, fmt.Errorf("--to: %w", err)
	}

	need, carry := bits.Add64(amount, fee, 0)
	if carry != 0 {
		return domain.Transaction{}, errAmountOverflow
	}

	tx := domain.Transaction{Timestamp: now.UTC()}

	var sum uint64
	for _, u := range utxos {
		if sum >= need {
			break
		}

		sum, carry = bits.Add64(sum, u.Amount, 0)
		if carry != 0 {
			return domain.Transaction{}, errAmountOverflow
		}

		tx.Inputs = append(tx.Inputs, domain.TxIn{PrevTxID: u.Txid, OutIndex: u.Index})
	}

	if sum < need {
		return domain.Transaction{}, fmt.Errorf("%w: have %d, need %d", errInsufficientFunds, sum, need)
	}

	tx.Outputs = []domain.TxOut{{Amount: amount, Address: to}}
	if change := sum - need; change > 0 {
		tx.Outputs = append(tx.Outputs, domain.TxOut{Amount: change, Address: w.Address()})
	}

	for i := range tx.Inputs {
		err := domain.SignInput(&tx, i, w)
		if err != nil {
			return domain.Transaction{}, err
		}
	}

	tx.ID = tx.CalculateHash()

	return tx, nil
}
