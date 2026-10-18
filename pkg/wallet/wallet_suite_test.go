package wallet

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestWallet(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Wallet Suite")
}

// Faster scrypt for tests; the on-disk format is unchanged.
var _ = BeforeSuite(func() {
	scryptN = 1 << 10
})
