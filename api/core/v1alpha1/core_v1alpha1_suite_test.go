package v1alpha1

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCoreV1alpha1(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Core V1alpha1 Suite")
}
