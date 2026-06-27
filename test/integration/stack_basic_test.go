package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

var _ = Describe("Stack basics", func() {

	Context("Simple Stack", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a single-resource Stack and reach Ready", func() {
			swr := fixtures.SimpleStack("simple-stack")

			By("Creating the Stack CR")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			By("Waiting for Stack to become Ready")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyStack.Status.Phase).To(Equal(corev1alpha1.StackReady))
		})

		It("should create the child StackResource with Available=True", func() {
			srName := stack.Spec.ResourceNames[0]
			sr, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.StackResourceIsAvailable(sr)).To(BeTrue())
		})

		It("should create a Deployment for the StackResource", func() {
			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.25-alpine"))
		})

		It("should create a Service for the StackResource", func() {
			srName := stack.Spec.ResourceNames[0]
			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Spec.Ports).NotTo(BeEmpty())
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(80)))
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Multi-resource Stack with cross-resource env", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a multi-resource Stack and reach Ready", func() {
			swr := fixtures.MultiResourceStack("multi-stack")

			By("Creating the Stack CR")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			By("Waiting for Stack to become Ready")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyStack.Status.Phase).To(Equal(corev1alpha1.StackReady))
		})

		It("should have all StackResources in Available state", func() {
			for _, name := range stack.Spec.ResourceNames {
				sr, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
					Name:      name,
					Namespace: stack.Namespace,
				}, readyTimeout)
				Expect(err).NotTo(HaveOccurred(), "StackResource %s should be Available", name)
				Expect(helpers.StackResourceIsAvailable(sr)).To(BeTrue())
			}
		})

		It("should have BACKEND_URL env var set to the backend resource name", func() {
			frontendName := stack.Spec.ResourceNames[1]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, frontendName)
			Expect(err).NotTo(HaveOccurred())

			backendName := stack.Spec.ResourceNames[0]
			val, found := helpers.GetContainerEnvVar(dep, "BACKEND_URL")
			Expect(found).To(BeTrue(), "BACKEND_URL env var should exist")
			Expect(val).To(Equal(backendName), "BACKEND_URL should equal the backend resource name")
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Stack with env vars and ports", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a Stack with env vars and multiple ports", func() {
			swr := fixtures.StackWithEnvAndPorts("envport-stack")

			By("Creating the Stack CR")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			By("Waiting for Stack to become Ready")
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have correct env vars on the Deployment", func() {
			resourceName := stack.Spec.ResourceNames[0]
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
			resourceName := stack.Spec.ResourceNames[0]
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
			resourceName := stack.Spec.ResourceNames[0]
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

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Stack with init container", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a Stack with an init container and reach Ready", func() {
			swr := fixtures.StackWithInitContainer("init-stack")

			By("Creating the Stack CR")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			By("Waiting for Stack to become Ready")
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have an init container in the Deployment", func() {
			resourceName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())

			Expect(dep.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initContainer := dep.Spec.Template.Spec.InitContainers[0]
			Expect(initContainer.Command).To(Equal([]string{"sh"}))
			Expect(initContainer.Args).To(Equal([]string{"-c", "echo 'init done'"}))
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Owner references and resource naming", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.SimpleStack("owner-ref-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should set Stack as owner of StackResource", func() {
			srName := stack.Spec.ResourceNames[0]
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.HasOwnerReference(sr.ObjectMeta, stack.Name, "Stack")).To(BeTrue())
		})

		It("should set StackResource as owner of Deployment", func() {
			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.HasOwnerReference(dep.ObjectMeta, srName, "StackResource")).To(BeTrue())
		})

		It("should set StackResource as owner of Service", func() {
			srName := stack.Spec.ResourceNames[0]
			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.HasOwnerReference(svc.ObjectMeta, srName, "StackResource")).To(BeTrue())
		})

		It("should have matching Deployment name, Service name, and StackResource name", func() {
			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Name).To(Equal(srName))

			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Name).To(Equal(srName))
		})

		It("should have pod label matching the Service selector", func() {
			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.Template.Labels).To(HaveKeyWithValue("resource", srName))

			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Spec.Selector).To(HaveKeyWithValue("resource", srName))
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("StackResource status fields", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.SimpleStack("status-fields-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have InternalAddress set to the StackResource name", func() {
			srName := stack.Spec.ResourceNames[0]
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.InternalAddress).NotTo(BeNil())
			Expect(*sr.Status.InternalAddress).To(Equal(srName))
		})

		It("should have Phase set to Ready", func() {
			srName := stack.Spec.ResourceNames[0]
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.Phase).To(Equal(corev1alpha1.StackResourcePhaseReady))
		})

		It("should have ObservedGeneration matching Generation", func() {
			srName := stack.Spec.ResourceNames[0]
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.ObservedGeneration).To(Equal(sr.Generation))
		})

		It("should have a non-empty StatusHash", func() {
			srName := stack.Spec.ResourceNames[0]
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
			cond := meta.FindStatusCondition(readyStack.Status.Conditions, string(corev1alpha1.StackConditionAvailable))
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).NotTo(BeEmpty())
			Expect(cond.Message).NotTo(BeEmpty())
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Command and Args overrides", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.StackWithCommandArgs("cmd-args-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have the correct Command and Args on the container", func() {
			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())

			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := dep.Spec.Template.Spec.Containers[0]
			Expect(container.Command).To(Equal([]string{"nginx"}))
			Expect(container.Args).To(Equal([]string{"-g", "daemon off;"}))
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Ingress for public ports", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.StackWithPublicPorts("ingress-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create an Ingress for the public port", func() {
			srName := stack.Spec.ResourceNames[0]
			ingress, err := helpers.GetIngressForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(ingress.Name).To(Equal(srName + "-http-proxy"))
		})

		It("should have correct Ingress rules", func() {
			srName := stack.Spec.ResourceNames[0]
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
			srName := stack.Spec.ResourceNames[0]
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: stack.Namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.ExternalAddress).To(HaveLen(1))
			Expect(sr.Status.ExternalAddress[0].TargetPort).To(Equal(int32(80)))
			Expect(sr.Status.ExternalAddress[0].Address).To(Equal("http://" + srName + ".example.com"))
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Resource with no ports (headless service)", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.StackWithNoPorts("noport-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create a headless Service", func() {
			srName := stack.Spec.ResourceNames[0]
			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.ServiceIsHeadless(svc)).To(BeTrue())
		})

		It("should not create an Ingress", func() {
			srName := stack.Spec.ResourceNames[0]
			_, err := helpers.GetIngressForResource(ctx, c, stack.Namespace, srName)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "Ingress should not exist for a resource with no ports")
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Init container with custom image", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.StackWithInitCustomImage("initimg-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have the correct init container image and main container image", func() {
			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())

			Expect(dep.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.InitContainers[0].Image).To(Equal("busybox:1.36"))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.25-alpine"))
		})

		It("should have the correct init container name", func() {
			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())

			Expect(dep.Spec.Template.Spec.InitContainers[0].Name).To(Equal(srName + "-init"))
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Deployment strategy and image pull policy", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.SimpleStack("deploy-cfg-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should use RollingUpdate deployment strategy", func() {
			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.Strategy.Type).To(Equal(appsv1.RollingUpdateDeploymentStrategyType))
		})

		It("should use IfNotPresent image pull policy", func() {
			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.Template.Spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("Three-resource Stack", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.StackWithThreeResources("three-res-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should reach Stack Ready", func() {
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have Deployments and Services for all 3 resources", func() {
			for _, name := range stack.Spec.ResourceNames {
				By("Checking Deployment for " + name)
				Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, name)).To(BeTrue(),
					"Deployment should exist for %s", name)

				By("Checking Service for " + name)
				_, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, name)
				Expect(err).NotTo(HaveOccurred(), "Service should exist for %s", name)
			}
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})
})
