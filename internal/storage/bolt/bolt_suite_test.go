package bolt_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBolt(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Bolt Suite")
}
