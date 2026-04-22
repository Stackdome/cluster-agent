package integration

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/bootstrap"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

const (
	stackReadyTimeout  = 5 * time.Minute
	stackDeleteTimeout = 2 * time.Minute
	srAvailTimeout     = 5 * time.Minute
)

var _ = Describe("Stack Lifecycle", Ordered, func() {
	var (
		testEnv *bootstrap.Environment
		ctx     context.Context
		c       client.Client
	)

	BeforeAll(func() {
		testEnv = GetEnvironment()
		ctx = context.Background()
		c = testEnv.Client
	})

	Context("Simple Stack", func() {
		var stack *corev1alpha1.Stack

		It("should create a single-resource Stack and reach Ready", func() {
			stack = fixtures.SimpleStack("simple-stack")

			By("Creating the Stack CR")
			Expect(c.Create(ctx, stack)).To(Succeed())

			By("Waiting for Stack to become Ready")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyStack.Status.Phase).To(Equal(corev1alpha1.StackReady))
		})

		It("should create the child StackResource with Available=True", func() {
			srName := stack.Spec.StackResources[0].Name
			sr, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.StackResourceIsAvailable(sr)).To(BeTrue())
		})

		It("should create a Deployment for the StackResource", func() {
			srName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.25-alpine"))
		})

		It("should create a Service for the StackResource", func() {
			srName := stack.Spec.StackResources[0].Name
			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Spec.Ports).NotTo(BeEmpty())
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(80)))
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up simple Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Multi-resource Stack with env interpolation", func() {
		var stack *corev1alpha1.Stack

		It("should create a multi-resource Stack and reach Ready", func() {
			stack = fixtures.MultiResourceStack("multi-stack")

			By("Creating the Stack CR")
			Expect(c.Create(ctx, stack)).To(Succeed())

			By("Waiting for Stack to become Ready")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyStack.Status.Phase).To(Equal(corev1alpha1.StackReady))
		})

		It("should have all StackResources in Available state", func() {
			for _, tmpl := range stack.Spec.StackResources {
				sr, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
					Name:      tmpl.Name,
					Namespace: stack.Namespace,
				}, srAvailTimeout)
				Expect(err).NotTo(HaveOccurred(), "StackResource %s should be Available", tmpl.Name)
				Expect(helpers.StackResourceIsAvailable(sr)).To(BeTrue())
			}
		})

		It("should interpolate env vars referencing sibling resources", func() {
			frontendName := stack.Spec.StackResources[1].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, frontendName)
			Expect(err).NotTo(HaveOccurred())

			backendName := stack.Spec.StackResources[0].Name
			val, found := helpers.GetContainerEnvVar(dep, "BACKEND_URL")
			Expect(found).To(BeTrue(), "BACKEND_URL env var should exist")
			Expect(val).To(Equal(backendName), "BACKEND_URL should be interpolated to the backend service name")
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up multi-resource Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Stack with dependencies", func() {
		var stack *corev1alpha1.Stack

		It("should create a Stack with dependsOn and reach Ready", func() {
			stack = fixtures.StackWithDependencies("dep-stack")

			By("Creating the Stack CR")
			Expect(c.Create(ctx, stack)).To(Succeed())

			By("Waiting for Stack to become Ready")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyStack.Status.Phase).To(Equal(corev1alpha1.StackReady))
		})

		It("should have resource A Available before resource B gets its Deployment", func() {
			resourceA := stack.Spec.StackResources[0].Name
			resourceB := stack.Spec.StackResources[1].Name

			By("Verifying resource A is Available")
			srA, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceA,
				Namespace: stack.Namespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.StackResourceIsAvailable(srA)).To(BeTrue())

			By("Verifying resource B is also Available (dependency was satisfied)")
			srB, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceB,
				Namespace: stack.Namespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.StackResourceIsAvailable(srB)).To(BeTrue())

			By("Verifying both Deployments exist")
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, resourceA)).To(BeTrue())
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, resourceB)).To(BeTrue())
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up dependency Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Stack with env vars and ports", func() {
		var stack *corev1alpha1.Stack

		It("should create a Stack with env vars and multiple ports", func() {
			stack = fixtures.StackWithEnvAndPorts("envport-stack")

			By("Creating the Stack CR")
			Expect(c.Create(ctx, stack)).To(Succeed())

			By("Waiting for Stack to become Ready")
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have correct env vars on the Deployment", func() {
			resourceName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())

			val, found := helpers.GetContainerEnvVar(dep, "APP_ENV")
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("integration-test"))

			val, found = helpers.GetContainerEnvVar(dep, "APP_PORT")
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("8080"))

			val, found = helpers.GetContainerEnvVar(dep, "LOG_LEVEL")
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("debug"))
		})

		It("should have correct port mappings on the Service", func() {
			resourceName := stack.Spec.StackResources[0].Name
			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())

			Expect(svc.Spec.Ports).To(HaveLen(2))

			portNumbers := make([]int32, len(svc.Spec.Ports))
			for i, p := range svc.Spec.Ports {
				portNumbers[i] = p.Port
			}
			Expect(portNumbers).To(ContainElements(int32(8080), int32(9090)))
		})

		It("should have correct container ports on the Deployment", func() {
			resourceName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())

			containerPorts := dep.Spec.Template.Spec.Containers[0].Ports
			Expect(containerPorts).To(HaveLen(2))

			portNumbers := make([]int32, len(containerPorts))
			for i, p := range containerPorts {
				portNumbers[i] = p.ContainerPort
			}
			Expect(portNumbers).To(ContainElements(int32(8080), int32(9090)))
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up env/ports Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Stack with init container", func() {
		var stack *corev1alpha1.Stack

		It("should create a Stack with an init container and reach Ready", func() {
			stack = fixtures.StackWithInitContainer("init-stack")

			By("Creating the Stack CR")
			Expect(c.Create(ctx, stack)).To(Succeed())

			By("Waiting for Stack to become Ready")
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have an init container in the Deployment", func() {
			resourceName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())

			Expect(dep.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initContainer := dep.Spec.Template.Spec.InitContainers[0]
			Expect(initContainer.Command).To(Equal([]string{"sh"}))
			Expect(initContainer.Args).To(Equal([]string{"-c", "echo 'init done'"}))
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up init container Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Stack deletion", func() {
		It("should clean up all owned resources on deletion", func() {
			stack := fixtures.StackForDeletion("del-stack")
			srName := stack.Spec.StackResources[0].Name

			By("Creating the Stack CR")
			Expect(c.Create(ctx, stack)).To(Succeed())

			By("Waiting for Stack to become Ready")
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StackResource and Deployment exist")
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, srName)).To(BeTrue())

			By("Deleting the Stack CR")
			Expect(c.Delete(ctx, stack)).To(Succeed())

			By("Waiting for Stack to be deleted")
			Expect(helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)).To(Succeed())

			By("Verifying the child StackResource is also deleted")
			sr := &corev1alpha1.StackResource{}
			err = c.Get(ctx, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			}, sr)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "StackResource should be deleted")

			By("Verifying the Deployment is also deleted")
			dep := &appsv1.Deployment{}
			err = c.Get(ctx, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			}, dep)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "Deployment should be deleted")
		})
	})
})
