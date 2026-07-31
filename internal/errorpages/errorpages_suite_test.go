package errorpages

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestErrorPages(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Error Pages Suite")
}
