package chain_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestChain(t *testing.T) {
	RegisterFailHandler(Fail)

	suite, reporter := GinkgoConfiguration()
	// A mutated lock can leak and hang the later specs; stop at the first failure instead.
	suite.FailFast = true
	RunSpecs(t, "Chain Suite", suite, reporter)
}
