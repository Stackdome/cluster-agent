package portcheck

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPortcheck(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Portcheck Suite")
}
