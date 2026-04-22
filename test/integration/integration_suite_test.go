package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"stackdome.io/cluster-agent/test/integration/bootstrap"
)

var env = &bootstrap.Environment{}

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration Test Suite")
}

var _ = BeforeSuite(func() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	err := bootstrap.Setup(env, ctx)
	if err != nil {
		cancel()
		Fail(fmt.Sprintf("Failed to bootstrap integration test environment: %v", err))
	}
	DeferCleanup(func() {
		cancel()
		env.Cleanup()
	})
})

func GetEnvironment() *bootstrap.Environment {
	return env
}
