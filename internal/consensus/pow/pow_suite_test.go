package pow_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPow(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Pow Suite")
}
