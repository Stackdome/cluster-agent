package imagebuild

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestImageBuild(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ImageBuild Controller Suite")
}
