package imagebuilder

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestImagebuilder(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Imagebuilder Suite")
}
