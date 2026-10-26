package domain

import (
	"encoding/hex"
	"errors"
)

// Limits of the contract VM (ADR-0014).
const (
	// MaxContractCodeBytes is the longest contract bytecode accepted.
	MaxContractCodeBytes = 1024
	// MaxContractGas is the largest gas limit a contract execution may ask for.
	MaxContractGas = 100000
)

// Contract operation kinds.
const (
	ContractDeploy = "deploy"
	ContractCall   = "call"
)

// ErrInvalidContractOp is returned for a structurally invalid ContractOp.
var ErrInvalidContractOp = errors.New("invalid contract operation")

// ContractOp is the contract operation a transaction may carry (ADR-0014).
// A deploy carries Code (canonical lower-case hex) and no Target; a call
// carries Target (the contract ID, 64 hex characters) and no Code.
type ContractOp struct {
	Kind     string
	Code     string
	Target   string
	Args     []uint64
	GasLimit uint64
}

// ValidateContractOp checks the structural rules of op; nil is valid.
func ValidateContractOp(op *ContractOp) error {
	if op == nil {
		return nil
	}

	if op.GasLimit == 0 || op.GasLimit > MaxContractGas {
		return ErrInvalidContractOp
	}

	switch op.Kind {
	case ContractDeploy:
		raw, ok := canonicalHex(op.Code)
		if op.Target != "" || !ok || len(raw) == 0 || len(raw) > MaxContractCodeBytes {
			return ErrInvalidContractOp
		}
	case ContractCall:
		if _, ok := canonicalHex(op.Target); op.Code != "" || !ok || len(op.Target) != 64 {
			return ErrInvalidContractOp
		}
	default:
		return ErrInvalidContractOp
	}

	return nil
}

// canonicalHex decodes s when it is lower-case hex with no odd digit.
func canonicalHex(s string) ([]byte, bool) {
	raw, err := hex.DecodeString(s)
	return raw, err == nil && hex.EncodeToString(raw) == s
}
