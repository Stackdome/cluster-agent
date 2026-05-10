package integration

import (
	"context"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/bootstrap"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

const (
	stackReadyTimeout  = 5 * time.Minute
	stackDeleteTimeout = 2 * time.Minute
	srAvailTimeout     = 5 * time.Minute

	buildReadyTimeout   = 10 * time.Minute
	buildJobTimeout     = 3 * time.Minute
	imageBuildTimeout   = 2 * time.Minute
	gitSecretName       = "git-credentials"
	buildArgSecretName  = "build-arg-secret"
	buildArgSecretValue = "test-secret-token-value"
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

	Context("Owner references and resource naming", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.SimpleStack("owner-ref-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should set Stack as owner of StackResource", func() {
			srName := stack.Spec.StackResources[0].Name
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.HasOwnerReference(sr.ObjectMeta, stack.Name, "Stack")).To(BeTrue())
		})

		It("should set StackResource as owner of Deployment", func() {
			srName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.HasOwnerReference(dep.ObjectMeta, srName, "StackResource")).To(BeTrue())
		})

		It("should set StackResource as owner of Service", func() {
			srName := stack.Spec.StackResources[0].Name
			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.HasOwnerReference(svc.ObjectMeta, srName, "StackResource")).To(BeTrue())
		})

		It("should have matching Deployment name, Service name, and StackResource name", func() {
			srName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Name).To(Equal(srName))

			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Name).To(Equal(srName))
		})

		It("should have pod label matching the Service selector", func() {
			srName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.Template.Labels).To(HaveKeyWithValue("resource", srName))

			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Spec.Selector).To(HaveKeyWithValue("resource", srName))
		})

		AfterAll(func() {
			if stack != nil {
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("StackResource status fields", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.SimpleStack("status-fields-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have InternalAddress set to the StackResource name", func() {
			srName := stack.Spec.StackResources[0].Name
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.InternalAddress).NotTo(BeNil())
			Expect(*sr.Status.InternalAddress).To(Equal(srName))
		})

		It("should have Phase set to Ready", func() {
			srName := stack.Spec.StackResources[0].Name
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.Phase).To(Equal(corev1alpha1.StackResourcePhaseReady))
		})

		It("should have ObservedGeneration matching Generation", func() {
			srName := stack.Spec.StackResources[0].Name
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.ObservedGeneration).To(Equal(sr.Generation))
		})

		It("should have a non-empty StatusHash", func() {
			srName := stack.Spec.StackResources[0].Name
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.StatusHash).NotTo(BeEmpty())
		})

		It("should have Available condition on the Stack with Reason and Message", func() {
			readyStack := &corev1alpha1.Stack{}
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), readyStack)).To(Succeed())
			cond := meta.FindStatusCondition(readyStack.Status.Conditions, string(corev1alpha1.StackAvailable))
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).NotTo(BeEmpty())
			Expect(cond.Message).NotTo(BeEmpty())
		})

		AfterAll(func() {
			if stack != nil {
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Command and Args overrides", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackWithCommandArgs("cmd-args-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have the correct Command and Args on the container", func() {
			srName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())

			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := dep.Spec.Template.Spec.Containers[0]
			Expect(container.Command).To(Equal([]string{"nginx"}))
			Expect(container.Args).To(Equal([]string{"-g", "daemon off;"}))
		})

		AfterAll(func() {
			if stack != nil {
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Ingress for public ports", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackWithPublicPorts("ingress-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create an Ingress for the public port", func() {
			srName := stack.Spec.StackResources[0].Name
			ingress, err := helpers.GetIngressForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(ingress.Name).To(Equal(srName + "-http-proxy"))
		})

		It("should have correct Ingress rules", func() {
			srName := stack.Spec.StackResources[0].Name
			ingress, err := helpers.GetIngressForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())

			Expect(ingress.Spec.Rules).NotTo(BeEmpty())
			rule := ingress.Spec.Rules[0]
			Expect(rule.Host).To(Equal(srName + ".example.com"))
			Expect(rule.HTTP.Paths).NotTo(BeEmpty())
			Expect(rule.HTTP.Paths[0].Backend.Service.Name).To(Equal(srName))
			Expect(rule.HTTP.Paths[0].Backend.Service.Port.Number).To(Equal(int32(80)))
		})

		It("should have correct ExternalAddress on the StackResource status", func() {
			srName := stack.Spec.StackResources[0].Name
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.ExternalAddress).To(HaveLen(1))
			Expect(sr.Status.ExternalAddress[0].TargetPort).To(Equal(int32(80)))
			Expect(sr.Status.ExternalAddress[0].Address).To(Equal(srName + ".example.com"))
		})

		AfterAll(func() {
			if stack != nil {
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Resource with no ports (headless service)", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackWithNoPorts("noport-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create a headless Service", func() {
			srName := stack.Spec.StackResources[0].Name
			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.ServiceIsHeadless(svc)).To(BeTrue())
		})

		It("should not create an Ingress", func() {
			srName := stack.Spec.StackResources[0].Name
			_, err := helpers.GetIngressForResource(ctx, c, stack.Namespace, srName)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "Ingress should not exist for a resource with no ports")
		})

		AfterAll(func() {
			if stack != nil {
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Dependency chain (A -> B -> C)", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackWithDependencyChain("chain-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should make resource A Available first", func() {
			resourceA := stack.Spec.StackResources[0].Name
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceA,
				Namespace: stack.Namespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should make resource B Available after A", func() {
			resourceB := stack.Spec.StackResources[1].Name
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceB,
				Namespace: stack.Namespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should make resource C Available after B", func() {
			resourceC := stack.Spec.StackResources[2].Name
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceC,
				Namespace: stack.Namespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reach Stack Ready", func() {
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			if stack != nil {
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Multiple dependencies (fan-in)", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackWithFanInDependencies("fanin-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should make resources A and B Available", func() {
			resourceA := stack.Spec.StackResources[0].Name
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceA,
				Namespace: stack.Namespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())

			resourceB := stack.Spec.StackResources[1].Name
			_, err = helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceB,
				Namespace: stack.Namespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should make resource C Available after both A and B", func() {
			resourceC := stack.Spec.StackResources[2].Name
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceC,
				Namespace: stack.Namespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reach Stack Ready", func() {
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			if stack != nil {
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Init container with custom image", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackWithInitCustomImage("initimg-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have the correct init container image and main container image", func() {
			srName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())

			Expect(dep.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.InitContainers[0].Image).To(Equal("busybox:1.36"))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.25-alpine"))
		})

		It("should have the correct init container name", func() {
			srName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())

			Expect(dep.Spec.Template.Spec.InitContainers[0].Name).To(Equal(srName + "-init"))
		})

		AfterAll(func() {
			if stack != nil {
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Deployment strategy and image pull policy", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.SimpleStack("deploy-cfg-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should use RollingUpdate deployment strategy", func() {
			srName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.Strategy.Type).To(Equal(appsv1.RollingUpdateDeploymentStrategyType))
		})

		It("should use IfNotPresent image pull policy", func() {
			srName := stack.Spec.StackResources[0].Name
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.Template.Spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
		})

		AfterAll(func() {
			if stack != nil {
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Restart mechanism", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackForRestart("restart-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should restart the Deployment when RestartRequest is set", func() {
			srName := stack.Spec.StackResources[0].Name

			By("Setting RestartRequest on the Stack template")
			now := metav1.Now()
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())
			stack.Spec.StackResources[0].Spec.RestartRequest = &now
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for Deployment to get restartedAt annotation")
			_, err := helpers.WaitForDeploymentUpdated(ctx, c, stack.Namespace, srName, func(d *appsv1.Deployment) bool {
				_, found := helpers.GetPodTemplateAnnotation(d, "kubectl.kubernetes.io/restartedAt")
				return found
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should set LastRestartRequestProcessedAt on the StackResource status", func() {
			srName := stack.Spec.StackResources[0].Name
			Eventually(func() bool {
				sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
					Name:      srName,
					Namespace: stack.Namespace,
				})
				if err != nil {
					return false
				}
				return sr.Status.LastRestartRequestProcessedAt != nil
			}, srAvailTimeout, 5*time.Second).Should(BeTrue())
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up restart Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Spec mutation - image update", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackForMutation("mut-image-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should update the Deployment image when the Stack image is changed", func() {
			newImage := "nginx:1.24-alpine"

			By("Re-getting the Stack from the API server")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())

			By("Updating the image in the Stack spec")
			stack.Spec.StackResources[0].Spec.ImageSpec.Image = newImage
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for Deployment to use the new image")
			srName := stack.Spec.StackResources[0].Name
			_, err := helpers.WaitForDeploymentUpdated(ctx, c, stack.Namespace, srName, func(d *appsv1.Deployment) bool {
				if len(d.Spec.Template.Spec.Containers) == 0 {
					return false
				}
				return d.Spec.Template.Spec.Containers[0].Image == newImage
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up image mutation Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Spec mutation - add env vars", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackForMutation("mut-env-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should update the Deployment env vars when added to the Stack", func() {
			By("Re-getting the Stack from the API server")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())

			By("Adding environment variables to the Stack spec")
			stack.Spec.StackResources[0].Spec.EnvironmentVariables = []corev1alpha1.EnvironmentVariables{
				{Name: "NEW_VAR", Value: "new-value"},
				{Name: "ANOTHER_VAR", Value: "another-value"},
			}
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for Deployment to have the new env vars")
			srName := stack.Spec.StackResources[0].Name
			dep, err := helpers.WaitForDeploymentUpdated(ctx, c, stack.Namespace, srName, func(d *appsv1.Deployment) bool {
				_, found := helpers.GetContainerEnvVar(d, "NEW_VAR")
				return found
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying both env var values")
			val, found := helpers.GetContainerEnvVar(dep, "NEW_VAR")
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("new-value"))

			val, found = helpers.GetContainerEnvVar(dep, "ANOTHER_VAR")
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("another-value"))
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up env mutation Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Spec mutation - add resource to Stack", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackForMutation("mut-addres-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create a new StackResource when a resource is added to the Stack", func() {
			secondResourceName := "mut-addres-stack-api"

			By("Re-getting the Stack from the API server")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())

			By("Appending a second StackResourceTemplate")
			stack.Spec.StackResources = append(stack.Spec.StackResources, corev1alpha1.StackResourceTemplate{
				Name: secondResourceName,
				Spec: corev1alpha1.StackResourceSpec{
					ImageSpec: &corev1alpha1.ImageSpec{
						Image: "nginx:1.25-alpine",
					},
					Ports: []corev1alpha1.Port{
						{
							Number: 8080,
							FQDN:   secondResourceName + ".local",
						},
					},
				},
			})
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for the second StackResource to become Available")
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      secondResourceName,
				Namespace: stack.Namespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Deployment exists for the second resource")
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, secondResourceName)).To(BeTrue())

			By("Waiting for Stack to reach Ready")
			_, err = helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up add-resource mutation Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Three-resource Stack", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.StackWithThreeResources("three-res-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should reach Stack Ready", func() {
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have Deployments and Services for all 3 resources", func() {
			for _, tmpl := range stack.Spec.StackResources {
				By("Checking Deployment for " + tmpl.Name)
				Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, tmpl.Name)).To(BeTrue(),
					"Deployment should exist for %s", tmpl.Name)

				By("Checking Service for " + tmpl.Name)
				_, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, tmpl.Name)
				Expect(err).NotTo(HaveOccurred(), "Service should exist for %s", tmpl.Name)
			}
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up three-resource Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("StackResource cascade deletion", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.SimpleStack("cascade-del-stack")
			Expect(c.Create(ctx, stack)).To(Succeed())
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), stackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should delete owned Deployment and Service when StackResource is deleted", func() {
			srName := stack.Spec.StackResources[0].Name

			By("Verifying Deployment and Service exist")
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, srName)).To(BeTrue())
			_, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())

			By("Removing the resource from the Stack template")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())
			stack.Spec.StackResources = []corev1alpha1.StackResourceTemplate{}
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for StackResource to be deleted")
			Expect(helpers.WaitForStackResourceDeleted(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			}, stackDeleteTimeout)).To(Succeed())

			By("Verifying Deployment is deleted")
			Eventually(func() bool {
				return !helpers.DeploymentExists(ctx, c, stack.Namespace, srName)
			}, stackDeleteTimeout, 5*time.Second).Should(BeTrue())

			By("Verifying Service is deleted")
			Eventually(func() bool {
				_, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
				return errors.IsNotFound(err)
			}, stackDeleteTimeout, 5*time.Second).Should(BeTrue())
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up cascade deletion Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("Build from Source with BuildArgs", func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			githubToken := os.Getenv("GITHUB_TOKEN")
			if githubToken == "" {
				Skip("GITHUB_TOKEN not set — skipping build args tests")
			}

			By("Creating git credentials secret")
			gitSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      gitSecretName,
					Namespace: testEnv.TestNamespace,
				},
				StringData: map[string]string{
					"username": "x-access-token",
					"token":    githubToken,
				},
			}
			Expect(c.Create(ctx, gitSecret)).To(Succeed())

			By("Creating build arg secret")
			argSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      buildArgSecretName,
					Namespace: testEnv.TestNamespace,
				},
				StringData: map[string]string{
					"token": buildArgSecretValue,
				},
			}
			Expect(c.Create(ctx, argSecret)).To(Succeed())
		})

		It("should create a Stack with BuildSpec and BuildArgs", func() {
			stack = fixtures.StackWithBuildArgs(
				"test-build-args",
				testEnv.RegistryURL,
				gitSecretName,
				buildArgSecretName,
			)

			By("Creating the Stack CR")
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should create an ImageBuild CR with BuildArgs propagated", func() {
			By("Waiting for ImageBuild to be created")
			imageBuild, err := helpers.WaitForImageBuildCreated(
				ctx, c, testEnv.TestNamespace, fixtures.BuildSourceResourceName, imageBuildTimeout,
			)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying BuildArgs are propagated to the ImageBuild CR")
			Expect(imageBuild.Spec.BuildArgs).To(HaveLen(2))

			var foundInline, foundSecret bool
			for _, arg := range imageBuild.Spec.BuildArgs {
				if arg.Name == "APP_ENV" && arg.Value == "integration-test" {
					foundInline = true
				}
				if arg.Name == "BUILD_TOKEN" && arg.ValueFrom != nil {
					Expect(arg.ValueFrom.SecretKeyRef.Name).To(Equal(buildArgSecretName))
					Expect(arg.ValueFrom.SecretKeyRef.Key).To(Equal("token"))
					foundSecret = true
				}
			}
			Expect(foundInline).To(BeTrue(), "inline build arg APP_ENV should be propagated")
			Expect(foundSecret).To(BeTrue(), "secret-backed build arg BUILD_TOKEN should be propagated")
		})

		It("should create a Kaniko Job with --build-arg flags", func() {
			By("Getting the ImageBuild name")
			imageBuild, err := helpers.WaitForImageBuildCreated(
				ctx, c, testEnv.TestNamespace, fixtures.BuildSourceResourceName, imageBuildTimeout,
			)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the build Job to be created")
			job, err := helpers.WaitForBuildJob(ctx, c, testEnv.TestNamespace, imageBuild.Name, buildJobTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying --build-arg flags on the Kaniko container")
			Expect(helpers.JobHasBuildArg(job, "APP_ENV", "integration-test")).To(BeTrue(),
				"Job should have --build-arg=APP_ENV=integration-test")
			Expect(helpers.JobHasBuildArg(job, "BUILD_TOKEN", buildArgSecretValue)).To(BeTrue(),
				"Job should have --build-arg=BUILD_TOKEN=<resolved-secret-value>")
		})

		It("should complete the build and deploy the image", func() {
			By("Getting the ImageBuild")
			imageBuild, err := helpers.WaitForImageBuildCreated(
				ctx, c, testEnv.TestNamespace, fixtures.BuildSourceResourceName, imageBuildTimeout,
			)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for ImageBuild to complete successfully")
			completedBuild, err := helpers.WaitForImageBuildComplete(
				ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(completedBuild.Status.ImageUrl).NotTo(BeEmpty())

			By("Waiting for StackResource to become Available")
			_, err = helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      fixtures.BuildSourceResourceName,
				Namespace: testEnv.TestNamespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Deployment uses the built image from in-cluster registry")
			deploy, err := helpers.GetDeploymentForResource(ctx, c, testEnv.TestNamespace, fixtures.BuildSourceResourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(deploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring(testEnv.RegistryURL))
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up build args Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
			_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: gitSecretName, Namespace: testEnv.TestNamespace}})
			_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: buildArgSecretName, Namespace: testEnv.TestNamespace}})
		})
	})

	Context("BuildSpec - ImageBuild naming labels and ownership", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.SimpleBuildStack("naming-build", testEnv.RegistryURL)
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should create an ImageBuild with the correct name prefix", func() {
			srName := stack.Spec.StackResources[0].Name

			By("Waiting for ImageBuild to be created")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, testEnv.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ImageBuild name has the StackResource name prefix")
			Expect(strings.HasPrefix(imageBuild.Name, srName+"-")).To(BeTrue(),
				"ImageBuild name %q should have prefix %q", imageBuild.Name, srName+"-")
		})

		It("should have correct labels on the ImageBuild", func() {
			srName := stack.Spec.StackResources[0].Name
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, testEnv.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			Expect(imageBuild.Labels).To(HaveKeyWithValue("stackdome.io/component", "image-build"))
			Expect(imageBuild.Labels).To(HaveKeyWithValue("stackdome.io/part-of", srName))
		})

		It("should have owner reference to StackResource", func() {
			srName := stack.Spec.StackResources[0].Name
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, testEnv.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			Expect(helpers.HasOwnerReference(imageBuild.ObjectMeta, srName, "StackResource")).To(BeTrue(),
				"ImageBuild should have owner reference to StackResource %s", srName)
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up naming-build Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("BuildSpec - simple build without BuildArgs", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.SimpleBuildStack("simple-build", testEnv.RegistryURL)
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should create an ImageBuild with empty BuildArgs", func() {
			srName := stack.Spec.StackResources[0].Name

			By("Waiting for ImageBuild to be created")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, testEnv.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying BuildArgs are empty")
			Expect(imageBuild.Spec.BuildArgs).To(BeEmpty())
		})

		It("should complete the build successfully", func() {
			srName := stack.Spec.StackResources[0].Name
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, testEnv.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for ImageBuild to complete")
			_, err = helpers.WaitForImageBuildComplete(ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should deploy with the built image from the in-cluster registry", func() {
			srName := stack.Spec.StackResources[0].Name

			By("Waiting for StackResource to become Available")
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: testEnv.TestNamespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Deployment container image references the in-cluster registry")
			dep, err := helpers.GetDeploymentForResource(ctx, c, testEnv.TestNamespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring(testEnv.RegistryURL))
		})

		AfterAll(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println(helpers.DumpBuildDiagnostics(ctx, c, testEnv.KubeClient, testEnv.TestNamespace))
			}
			if stack != nil {
				By("Cleaning up simple-build Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("BuildSpec - CurrentBuild status fields", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.SimpleBuildStack("status-build", testEnv.RegistryURL)
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should populate CurrentBuild status after build completes", func() {
			srName := stack.Spec.StackResources[0].Name

			By("Waiting for ImageBuild to complete")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, testEnv.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())
			_, err = helpers.WaitForImageBuildComplete(ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for StackResource to become Available")
			sr, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: testEnv.TestNamespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying CurrentBuild status fields")
			Expect(sr.Status.CurrentBuild).NotTo(BeNil(), "CurrentBuild should not be nil")
			Expect(sr.Status.CurrentBuild.Name).NotTo(BeEmpty(), "CurrentBuild.Name should not be empty")
			Expect(sr.Status.CurrentBuild.Phase).To(Equal("Success"), "CurrentBuild.Phase should be Success")
			Expect(sr.Status.CurrentBuild.Available).To(BeTrue(), "CurrentBuild.Available should be true")

			By("Verifying ImageSourceRevision is set")
			Expect(sr.Status.ImageSourceRevision).NotTo(BeEmpty(), "ImageSourceRevision should not be empty")
		})

		AfterAll(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println(helpers.DumpBuildDiagnostics(ctx, c, testEnv.KubeClient, testEnv.TestNamespace))
			}
			if stack != nil {
				By("Cleaning up status-build Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("BuildSpec - custom Dockerfile and context paths", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.BuildStackCustomPaths("custpath-build", testEnv.RegistryURL)
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should create an ImageBuild with custom Dockerfile and context paths", func() {
			srName := stack.Spec.StackResources[0].Name

			By("Waiting for ImageBuild to be created")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, testEnv.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying custom Dockerfile path")
			Expect(imageBuild.Spec.BuildContext.DockerfilePath).To(Equal("docker/Dockerfile.prod"))

			By("Verifying custom context path")
			Expect(imageBuild.Spec.BuildContext.ContextPath).To(Equal("."))
		})

		It("should complete the build successfully", func() {
			srName := stack.Spec.StackResources[0].Name
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, testEnv.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for ImageBuild to complete")
			_, err = helpers.WaitForImageBuildComplete(ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println(helpers.DumpBuildDiagnostics(ctx, c, testEnv.KubeClient, testEnv.TestNamespace))
			}
			if stack != nil {
				By("Cleaning up custpath-build Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("BuildSpec - source revision update triggers new build", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.SimpleBuildStack("rev-update-build", testEnv.RegistryURL)
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should trigger a new build when source revision is updated", func() {
			srName := stack.Spec.StackResources[0].Name

			By("Waiting for initial ImageBuild to be created")
			firstBuild, err := helpers.WaitForImageBuildCreated(ctx, c, testEnv.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())
			firstBuildName := firstBuild.Name

			By("Waiting for initial build to complete and StackResource to become Available")
			_, err = helpers.WaitForImageBuildComplete(ctx, c, client.ObjectKeyFromObject(firstBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
			_, err = helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: testEnv.TestNamespace,
			}, srAvailTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Re-getting the Stack from the API server")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())

			By("Updating the source revision to trigger a new build")
			stack.Spec.StackResources[0].Spec.BuildSpec.SourceRevision.GitRepo.Branch.HeadSha = "new-sha"
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for a new ImageBuild to be created with a different name")
			Eventually(func() bool {
				list := &buildsv1alpha1.ImageBuildList{}
				if err := c.List(ctx, list, client.InNamespace(testEnv.TestNamespace)); err != nil {
					return false
				}
				for _, ib := range list.Items {
					if strings.HasPrefix(ib.Name, srName+"-") && ib.Name != firstBuildName {
						return true
					}
				}
				return false
			}, imageBuildTimeout, 5*time.Second).Should(BeTrue(),
				"A new ImageBuild with prefix %q and name != %q should be created", srName+"-", firstBuildName)
		})

		AfterAll(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println(helpers.DumpBuildDiagnostics(ctx, c, testEnv.KubeClient, testEnv.TestNamespace))
			}
			if stack != nil {
				By("Cleaning up rev-update-build Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("BuildSpec - build failure propagation", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.BuildStackBrokenDockerfile("fail-build", testEnv.RegistryURL)
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should have ImageBuild reach Failed phase", func() {
			srName := stack.Spec.StackResources[0].Name

			By("Waiting for ImageBuild to be created")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, testEnv.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for ImageBuild to fail")
			_, err = helpers.WaitForImageBuildFailed(ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should keep StackResource in Pending phase with Available=False", func() {
			srName := stack.Spec.StackResources[0].Name

			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: testEnv.TestNamespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.Phase).To(Equal(corev1alpha1.StackResourcePhasePending))
			Expect(helpers.StackResourceIsAvailable(sr)).To(BeFalse(),
				"StackResource should not be Available when build fails")
		})

		AfterAll(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println(helpers.DumpBuildDiagnostics(ctx, c, testEnv.KubeClient, testEnv.TestNamespace))
			}
			if stack != nil {
				By("Cleaning up fail-build Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("FailedContainerStatuses - crash detection", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.CrashingStack("crash-detect")
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should populate FailedContainerStatuses when container crashes", func() {
			srName := stack.Spec.StackResources[0].Name
			srKey := client.ObjectKey{Name: srName, Namespace: testEnv.TestNamespace}

			By("Waiting for FailedContainerStatuses to be populated")
			sr, err := helpers.WaitForFailedContainerStatuses(ctx, c, srKey, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(sr.Status.FailedContainerStatuses).NotTo(BeEmpty())
			fcs := sr.Status.FailedContainerStatuses[0]
			Expect(fcs.Name).To(Equal(srName))
			Expect(fcs.ExitCode).NotTo(BeNil())
			Expect(*fcs.ExitCode).To(Equal(int32(1)))
			Expect(fcs.State).To(BeElementOf("waiting", "terminated"))
			Expect(fcs.Logs).To(ContainSubstring("ERROR: connection refused"))

			By("Verifying ObservedDeploymentRevision is set")
			Expect(sr.Status.ObservedDeploymentRevision).NotTo(BeEmpty())
		})

		It("should clear FailedContainerStatuses when container recovers", func() {
			srName := stack.Spec.StackResources[0].Name
			srKey := client.ObjectKey{Name: srName, Namespace: testEnv.TestNamespace}

			By("Updating Stack to use a healthy image")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())
			stack.Spec.StackResources[0].Spec.ImageSpec.Image = "nginx:1.25-alpine"
			stack.Spec.StackResources[0].Spec.Command = nil
			stack.Spec.StackResources[0].Spec.Args = nil
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for StackResource to become Available")
			sr, err := helpers.WaitForStackResourceAvailable(ctx, c, srKey, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(sr.Status.FailedContainerStatuses).To(BeNil())
			Expect(sr.Status.ObservedDeploymentRevision).To(BeEmpty())
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up crash-detect Stack")
				_ = c.Delete(ctx, stack)
				_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), stackDeleteTimeout)
			}
		})
	})

	Context("FailedContainerStatuses - image pull failure", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			testEnv = GetEnvironment()
			ctx = context.Background()
			c = testEnv.Client

			stack = fixtures.ImagePullFailStack("imgpull-fail")
			Expect(c.Create(ctx, stack)).To(Succeed())
		})

		It("should populate FailedContainerStatuses with ImagePullBackOff", func() {
			srName := stack.Spec.StackResources[0].Name
			srKey := client.ObjectKey{Name: srName, Namespace: testEnv.TestNamespace}

			By("Waiting for FailedContainerStatuses to be populated")
			sr, err := helpers.WaitForFailedContainerStatuses(ctx, c, srKey, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(sr.Status.FailedContainerStatuses).NotTo(BeEmpty())
			fcs := sr.Status.FailedContainerStatuses[0]
			Expect(fcs.Reason).To(BeElementOf("ImagePullBackOff", "ErrImagePull"))
			Expect(fcs.ExitCode).To(BeNil())
			Expect(fcs.Logs).To(BeEmpty())
			Expect(fcs.Message).To(ContainSubstring("nonexistent-registry.example.com/fake-image"))
		})

		AfterAll(func() {
			if stack != nil {
				By("Cleaning up imgpull-fail Stack")
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
