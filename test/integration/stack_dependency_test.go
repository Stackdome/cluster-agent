package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

var _ = Describe("Stack dependency ordering", func() {

	Context("Stack with dependencies", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a Stack with dependsOn and reach Ready", func() {
			swr := fixtures.StackWithDependencies("dep-stack")

			By("Creating the Stack CR")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			By("Waiting for Stack to become Ready")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyStack.Status.Phase).To(Equal(corev1alpha1.StackReady))
		})

		It("should have resource A Available before resource B gets its Deployment", func() {
			resourceA := stack.Spec.ResourceNames[0]
			resourceB := stack.Spec.ResourceNames[1]

			By("Verifying resource A is Available")
			srA, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceA,
				Namespace: stack.Namespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.StackResourceIsAvailable(srA)).To(BeTrue())

			By("Verifying resource B is also Available (dependency was satisfied)")
			srB, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceB,
				Namespace: stack.Namespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.StackResourceIsAvailable(srB)).To(BeTrue())

			By("Verifying both Deployments exist")
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, resourceA)).To(BeTrue())
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, resourceB)).To(BeTrue())
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Dependency chain (A -> B -> C)", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.StackWithDependencyChain("chain-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should make resource A Available first", func() {
			resourceA := stack.Spec.ResourceNames[0]
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceA,
				Namespace: stack.Namespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should make resource B Available after A", func() {
			resourceB := stack.Spec.ResourceNames[1]
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceB,
				Namespace: stack.Namespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should make resource C Available after B", func() {
			resourceC := stack.Spec.ResourceNames[2]
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceC,
				Namespace: stack.Namespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reach Stack Ready", func() {
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Multiple dependencies (fan-in)", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.StackWithFanInDependencies("fanin-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should make resources A and B Available", func() {
			resourceA := stack.Spec.ResourceNames[0]
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceA,
				Namespace: stack.Namespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			resourceB := stack.Spec.ResourceNames[1]
			_, err = helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceB,
				Namespace: stack.Namespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should make resource C Available after both A and B", func() {
			resourceC := stack.Spec.ResourceNames[2]
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceC,
				Namespace: stack.Namespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reach Stack Ready", func() {
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})
})
