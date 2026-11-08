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

	sum, err := selectInputs(&tx, utxos, need)
	if err != nil {
		return domain.Transaction{}, err
	}

	tx.Outputs = []domain.TxOut{{Amount: amount, Address: to}}
	if change := sum - need; change > 0 {
		tx.Outputs = append(tx.Outputs, domain.TxOut{Amount: change, Address: w.Address()})
	}

	return signAndHash(tx, w)
}

// buildContractTx spends utxos of w, in order, until they cover fee+1 and pays
// everything but the fee back to w in a single output (contracts hold no
// coins, ADR-0014). op is set before signing, so the hash and every signature
// cover it.
func buildContractTx(w *wallet.Wallet, utxos []client.Utxo, op domain.ContractOp, fee uint64, now time.Time) (domain.Transaction, error) {
	need, carry := bits.Add64(fee, 1, 0)
	if carry != 0 {
		return domain.Transaction{}, errAmountOverflow
	}

	tx := domain.Transaction{Timestamp: now.UTC(), Contract: &op}

	sum, err := selectInputs(&tx, utxos, need)
	if err != nil {
		return domain.Transaction{}, err
	}

	tx.Outputs = []domain.TxOut{{Amount: sum - fee, Address: w.Address()}}

	return signAndHash(tx, w)
}

// selectInputs appends to tx.Inputs the utxos, in order, until their sum
// covers need, and returns that sum.
func selectInputs(tx *domain.Transaction, utxos []client.Utxo, need uint64) (uint64, error) {
	var (
		sum   uint64
		carry uint64
	)

	for _, u := range utxos {
		if sum >= need {
			break
		}

		sum, carry = bits.Add64(sum, u.Amount, 0)
		if carry != 0 {
			return 0, errAmountOverflow
		}

		tx.Inputs = append(tx.Inputs, domain.TxIn{PrevTxID: u.Txid, OutIndex: u.Index})
	}

	if sum < need {
		return 0, fmt.Errorf("%w: have %d, need %d", errInsufficientFunds, sum, need)
	}

	return sum, nil
}

// signAndHash signs every input of tx, after its outputs and contract are
// final, and sets its ID.
func signAndHash(tx domain.Transaction, w *wallet.Wallet) (domain.Transaction, error) {
	for i := range tx.Inputs {
		err := domain.SignInput(&tx, i, w)
		if err != nil {
			return domain.Transaction{}, err
		}
	}

	tx.ID = tx.CalculateHash()

	return tx, nil
}
