package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

	Context("StatefulService workload type", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a StatefulSet and a headless Service", func() {
			swr := fixtures.NewStack("sts-test",
				fixtures.NewResource("sts-test", "sts-test-app",
					fixtures.WithWorkloadType(corev1alpha1.WorkloadTypeStatefulService),
					fixtures.WithPorts(corev1alpha1.Port{Name: "http", Number: 80, Protocol: "http", FQDN: "sts-test-app.local"})))
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			srName := stack.Spec.ResourceNames[0]

			By("Verifying StatefulSet was created with replicas=1")
			sts, err := helpers.GetStatefulSetForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(*sts.Spec.Replicas).To(Equal(int32(1)))
			Expect(sts.Spec.ServiceName).To(Equal(srName))
			Expect(sts.Spec.VolumeClaimTemplates).To(BeEmpty())

			By("Verifying headless Service was created")
			svc, err := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Spec.ClusterIP).To(Equal("None"))
			Expect(svc.Spec.Ports).To(HaveLen(1))

			By("Verifying no Deployment was created")
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, srName)).To(BeFalse())
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})

	Context("Job workload type", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a Job and report success", func() {
			swr := fixtures.NewStack("job-test",
				fixtures.NewResource("job-test", "job-test-runner",
					fixtures.WithWorkloadType(corev1alpha1.WorkloadTypeJob),
					fixtures.WithoutPorts(),
					fixtures.WithImage("busybox:latest"),
					fixtures.WithCommand([]string{"sh", "-c", "echo done"}, nil)))
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			srName := stack.Spec.ResourceNames[0]

			By("Waiting for StackResource to become available")
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{Name: srName, Namespace: stack.Namespace}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Job was created with completions=1")
			job, err := helpers.GetJobForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(*job.Spec.Completions).To(Equal(int32(1)))

			By("Waiting for Job completion")
			_, err = helpers.WaitForJobComplete(ctx, c, stack.Namespace, srName, readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying StackResource reports success")
			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{Name: srName, Namespace: stack.Namespace})
			Expect(err).NotTo(HaveOccurred())
			Expect(sr.Status.LastRunSucceeded).NotTo(BeNil())
			Expect(*sr.Status.LastRunSucceeded).To(BeTrue())

			By("Verifying no Service was created")
			_, svcErr := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(errors.IsNotFound(svcErr)).To(BeTrue())
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})

	Context("CronJob workload type", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should create a CronJob and report available", func() {
			swr := fixtures.NewStack("cronjob-test",
				fixtures.NewResource("cronjob-test", "cj-test-tick",
					fixtures.WithWorkloadType(corev1alpha1.WorkloadTypeCronJob),
					fixtures.WithoutPorts(),
					fixtures.WithImage("busybox:latest"),
					fixtures.WithSchedule("*/1 * * * *"),
					fixtures.WithCommand([]string{"sh", "-c", "echo tick"}, nil)))
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			srName := stack.Spec.ResourceNames[0]

			By("Waiting for StackResource to become available")
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{Name: srName, Namespace: stack.Namespace}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying CronJob was created with correct schedule")
			cj, err := helpers.GetCronJobForResource(ctx, c, stack.Namespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(cj.Spec.Schedule).To(Equal("*/1 * * * *"))
			Expect(cj.Spec.ConcurrencyPolicy).To(Equal(batchv1.ForbidConcurrent))

			By("Verifying no Service was created")
			_, svcErr := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(errors.IsNotFound(svcErr)).To(BeTrue())
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})
})
