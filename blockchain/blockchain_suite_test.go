package blockchain_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBlockchain(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Blockchain Suite")
}
