package domain

import "math/big"

// BlockWork is the expected work behind b: 2^Difficulty, since Difficulty counts
// the leading zero bits of the hash (ADR-0012). A block of difficulty 0 (every
// PoA block) weighs 1 and a nil block weighs 0. Callers must only weigh blocks
// their consensus has verified, since Difficulty is otherwise unbounded.
func BlockWork(b *Block) *big.Int {
	if b == nil {
		return new(big.Int)
	}
	return new(big.Int).Lsh(big.NewInt(1), uint(max(b.Difficulty, 0)))
}

// ChainWork is the sum of BlockWork over blocks.
func ChainWork(blocks []*Block) *big.Int {
	sum := new(big.Int)
	for _, b := range blocks {
		sum.Add(sum, BlockWork(b))
	}
	return sum
}
