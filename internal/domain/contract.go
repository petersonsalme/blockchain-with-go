package domain

// Limits of the contract VM (ADR-0014).
const (
	// MaxContractCodeBytes is the longest contract bytecode accepted.
	MaxContractCodeBytes = 1024
	// MaxContractGas is the largest gas limit a contract execution may ask for.
	MaxContractGas = 100000
)
