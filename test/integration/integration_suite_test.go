package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/test/integration/bootstrap"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

var (
	env = &bootstrap.Environment{}
	ctx = context.Background()
	c   client.Client
)

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration Test Suite")
}

var _ = BeforeSuite(func() {
	setupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	err := bootstrap.Setup(env, setupCtx)
	if err != nil {
		cancel()
		Fail(fmt.Sprintf("Failed to bootstrap integration test environment: %v", err))
	}
	c = env.Client
	DeferCleanup(func() {
		cancel()
		env.Cleanup()
	})
})

var _ = JustAfterEach(func() {
	if CurrentSpecReport().Failed() {
		GinkgoWriter.Println(helpers.DumpBuildDiagnostics(ctx, c, env.KubeClient, env.TestNamespace))
	}
})
