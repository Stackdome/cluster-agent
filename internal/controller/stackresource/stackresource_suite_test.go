package stackresource

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestStackResourceSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "StackResource Suite")
}
