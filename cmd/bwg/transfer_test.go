package main

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/client"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

func TestBuildTransferWithChange(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	to, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	utxos := []client.Utxo{
		{Txid: "a", Index: 0, Amount: 30},
		{Txid: "b", Index: 0, Amount: 20},
		{Txid: "c", Index: 0, Amount: 100},
	}

	tx, err := buildTransfer(w, utxos, to.Address(), 40, 5, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tx.Inputs) != 2 {
		t.Fatalf("expected 2 inputs, got %d", len(tx.Inputs))
	}

	if tx.Inputs[0].PrevTxID != "a" || tx.Inputs[0].OutIndex != 0 {
		t.Errorf("unexpected input[0]: %+v", tx.Inputs[0])
	}

	if tx.Inputs[1].PrevTxID != "b" || tx.Inputs[1].OutIndex != 0 {
		t.Errorf("unexpected input[1]: %+v", tx.Inputs[1])
	}

	if len(tx.Outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(tx.Outputs))
	}

	expectedOutputs := []domain.TxOut{
		{Amount: 40, Address: to.Address()},
		{Amount: 5, Address: w.Address()},
	}
	for i, exp := range expectedOutputs {
		if tx.Outputs[i] != exp {
			t.Errorf("output[%d] = %+v, want %+v", i, tx.Outputs[i], exp)
		}
	}
}

func TestBuildTransferExactCoverHasNoChange(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	to, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	utxos := []client.Utxo{
		{Txid: "a", Index: 0, Amount: 30},
		{Txid: "b", Index: 0, Amount: 20},
		{Txid: "c", Index: 0, Amount: 7},
	}

	tx, err := buildTransfer(w, utxos, to.Address(), 45, 5, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tx.Inputs) != 2 {
		t.Fatalf("expected 2 inputs, got %d", len(tx.Inputs))
	}

	if tx.Inputs[0].PrevTxID != "a" || tx.Inputs[0].OutIndex != 0 {
		t.Errorf("unexpected input[0]: %+v", tx.Inputs[0])
	}

	if tx.Inputs[1].PrevTxID != "b" || tx.Inputs[1].OutIndex != 0 {
		t.Errorf("unexpected input[1]: %+v", tx.Inputs[1])
	}

	if len(tx.Outputs) != 1 {
		t.Fatalf("expected 1 output, got %d", len(tx.Outputs))
	}

	expectedOutput := domain.TxOut{Amount: 45, Address: to.Address()}
	if tx.Outputs[0] != expectedOutput {
		t.Errorf("output[0] = %+v, want %+v", tx.Outputs[0], expectedOutput)
	}
}

func TestBuildTransferSignsEveryInput(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	to, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	utxos := []client.Utxo{
		{Txid: "a", Index: 0, Amount: 30},
		{Txid: "b", Index: 0, Amount: 20},
		{Txid: "c", Index: 0, Amount: 100},
	}

	tx, err := buildTransfer(w, utxos, to.Address(), 40, 5, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i := range tx.Inputs {
		spent := domain.TxOut{Amount: utxos[i].Amount, Address: w.Address()}
		if err := domain.VerifyInputSignature(&tx, i, spent); err != nil {
			t.Errorf("VerifyInputSignature(input %d) failed: %v", i, err)
		}
	}

	if tx.ID != tx.CalculateHash() {
		t.Errorf("tx.ID = %q, want %q", tx.ID, tx.CalculateHash())
	}

	if err := tx.Validate(); err != nil {
		t.Errorf("tx.Validate() failed: %v", err)
	}

	if !tx.Timestamp.Equal(now) {
		t.Errorf("tx.Timestamp = %v, want %v", tx.Timestamp, now)
	}

	if tx.Timestamp.Location() != time.UTC {
		t.Errorf("tx.Timestamp.Location() = %v, want UTC", tx.Timestamp.Location())
	}
}

func TestBuildTransferInsufficientFunds(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	to, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	utxos := []client.Utxo{
		{Txid: "a", Index: 0, Amount: 10},
	}

	_, err = buildTransfer(w, utxos, to.Address(), 10, 1, now)
	if !errors.Is(err, errInsufficientFunds) {
		t.Fatalf("got error %v, want %v", err, errInsufficientFunds)
	}
}

func TestBuildTransferRejectsZeroAmount(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	to, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	utxos := []client.Utxo{
		{Txid: "a", Index: 0, Amount: 10},
	}

	_, err = buildTransfer(w, utxos, to.Address(), 0, 5, now)
	if !errors.Is(err, errZeroAmount) {
		t.Fatalf("got error %v, want %v", err, errZeroAmount)
	}
}

func TestBuildTransferRejectsInvalidRecipient(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	utxos := []client.Utxo{
		{Txid: "a", Index: 0, Amount: 10},
	}

	_, err = buildTransfer(w, utxos, "nope", 10, 1, now)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if errors.Is(err, errZeroAmount) || errors.Is(err, errInsufficientFunds) || errors.Is(err, errAmountOverflow) {
		t.Fatalf("unexpected sentinel error: %v", err)
	}
}

func TestBuildTransferRejectsAmountPlusFeeOverflow(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	to, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	utxos := []client.Utxo{
		{Txid: "a", Index: 0, Amount: 10},
	}

	_, err = buildTransfer(w, utxos, to.Address(), math.MaxUint64, 1, now)
	if !errors.Is(err, errAmountOverflow) {
		t.Fatalf("got error %v, want %v", err, errAmountOverflow)
	}
}

func TestBuildTransferRejectsInputSumOverflow(t *testing.T) {
	w, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	to, err := wallet.New()
	if err != nil {
		t.Fatalf("wallet.New failed: %v", err)
	}

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	utxos := []client.Utxo{
		{Txid: "a", Index: 0, Amount: math.MaxUint64 - 1},
		{Txid: "b", Index: 0, Amount: 2},
	}

	_, err = buildTransfer(w, utxos, to.Address(), math.MaxUint64, 0, now)
	if !errors.Is(err, errAmountOverflow) {
		t.Fatalf("got error %v, want %v", err, errAmountOverflow)
	}
}
