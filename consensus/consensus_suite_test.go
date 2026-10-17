package consensus_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestConsensus(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Consensus Suite")
}
