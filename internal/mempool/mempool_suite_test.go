package mempool_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMempool(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Mempool Suite")
}
