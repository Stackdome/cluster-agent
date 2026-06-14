package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

var _ = Describe("StackResource workload spec", func() {

	Context("valueFrom env var (secretKeyRef)", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a Deployment with secretKeyRef env var", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "test-app-secret", Namespace: env.TestNamespace},
				StringData: map[string]string{"API_KEY": "test-secret-value"},
			}
			Expect(c.Create(ctx, secret)).To(Succeed())

			swr := fixtures.SecretEnvStack("secret-env-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())

			container := dep.Spec.Template.Spec.Containers[0]
			var found bool
			for _, env := range container.Env {
				if env.Name == "API_KEY" {
					found = true
					Expect(env.ValueFrom).NotTo(BeNil())
					Expect(env.ValueFrom.SecretKeyRef).NotTo(BeNil())
					Expect(env.ValueFrom.SecretKeyRef.Name).To(Equal("test-app-secret"))
					Expect(env.ValueFrom.SecretKeyRef.Key).To(Equal("API_KEY"))
				}
			}
			Expect(found).To(BeTrue(), "API_KEY env var with secretKeyRef should exist")
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
			_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "test-app-secret", Namespace: env.TestNamespace}})
		})
	})

	Context("Health probes on Deployment", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a Deployment with readiness and startup probes", func() {
			swr := fixtures.ProbedStack("probed-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			srName := stack.Spec.ResourceNames[0]
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())

			container := dep.Spec.Template.Spec.Containers[0]
			Expect(container.ReadinessProbe).NotTo(BeNil())
			Expect(container.ReadinessProbe.HTTPGet).NotTo(BeNil())
			Expect(container.ReadinessProbe.HTTPGet.Path).To(Equal("/"))
			Expect(container.ReadinessProbe.HTTPGet.Port.IntValue()).To(Equal(80))

			Expect(container.StartupProbe).NotTo(BeNil())
			Expect(container.StartupProbe.HTTPGet).NotTo(BeNil())
			Expect(container.StartupProbe.FailureThreshold).To(Equal(int32(30)))
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})

	Context("Worker workload type", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a Deployment without a Service", func() {
			swr := fixtures.WorkerStack("worker-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			srName := stack.Spec.ResourceNames[0]
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, srName)).To(BeTrue())

			Consistently(func() bool {
				_, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
				return errors.IsNotFound(err)
			}, 15*time.Second, 5*time.Second).Should(BeTrue(), "Worker should never have a Service")
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})

	Context("Hardened security defaults", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should stamp pod and container security contexts on the Deployment", func() {
			swr := fixtures.NewStack("hardened-stack",
				fixtures.NewResource("hardened-stack", "hardened-stack-app",
					fixtures.WithImage("nginxinc/nginx-unprivileged:1.25-alpine"),
					fixtures.WithPorts(corev1alpha1.Port{Name: "http", Number: 8080, Protocol: "http", FQDN: "hardened-stack-app.local"}),
					fixtures.WithHardenedSecurity()))
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, "hardened-stack-app")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying pod security context")
			podSC := dep.Spec.Template.Spec.SecurityContext
			Expect(podSC).NotTo(BeNil())
			Expect(podSC.RunAsNonRoot).NotTo(BeNil())
			Expect(*podSC.RunAsNonRoot).To(BeTrue())
			Expect(podSC.SeccompProfile).NotTo(BeNil())
			Expect(podSC.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

			By("Verifying container security context")
			containerSC := dep.Spec.Template.Spec.Containers[0].SecurityContext
			Expect(containerSC).NotTo(BeNil())
			Expect(containerSC.AllowPrivilegeEscalation).NotTo(BeNil())
			Expect(*containerSC.AllowPrivilegeEscalation).To(BeFalse())
			Expect(containerSC.Capabilities).NotTo(BeNil())
			Expect(containerSC.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})

	Context("Unsupported workload type", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should set StackResource to Failed (not Pending)", func() {
			swr := fixtures.UnsupportedTypeStack("unsupported-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			srName := stack.Spec.ResourceNames[0]

			Eventually(func() corev1alpha1.StackResourcePhase {
				sr := &corev1alpha1.StackResource{}
				if err := c.Get(ctx, client.ObjectKey{Name: srName, Namespace: stack.Namespace}, sr); err != nil {
					return ""
				}
				return sr.Status.Phase
			}, readyTimeout, "5s").Should(Equal(corev1alpha1.StackResourcePhaseFailed))

			By("Verifying failure reason is WorkloadTypeNotSupported")
			sr := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: srName, Namespace: stack.Namespace}, sr)).To(Succeed())
			cond := meta.FindStatusCondition(sr.Status.Conditions, string(corev1alpha1.StackResourceStatusAvailable))
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("WorkloadTypeNotSupported"))

			By("Verifying Stalled condition is set")
			stalledCond := meta.FindStatusCondition(sr.Status.Conditions, string(corev1alpha1.StackResourceStalled))
			Expect(stalledCond).NotTo(BeNil())
			Expect(stalledCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(stalledCond.Reason).To(Equal("WorkloadTypeNotSupported"))

			Eventually(func() corev1alpha1.StackPhase {
				s := &corev1alpha1.Stack{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(stack), s); err != nil {
					return ""
				}
				return s.Status.Phase
			}, readyTimeout, "5s").Should(Equal(corev1alpha1.StackFailed))
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})
})
