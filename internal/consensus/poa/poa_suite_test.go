package poa_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPoa(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Poa Suite")
}
