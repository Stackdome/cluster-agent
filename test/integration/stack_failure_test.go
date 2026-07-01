package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

var _ = Describe("StackResource failure reporting", func() {

	Context("LastFailureDetails - crash detection", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.CrashingStack("crash-detect")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should populate LastFailureDetails when container crashes", func() {
			srName := stack.Spec.ResourceNames[0]
			srKey := client.ObjectKey{Name: srName, Namespace: env.TestNamespace}

			By("Waiting for LastFailureDetails to be populated")
			sr, err := helpers.WaitForLastFailureDetails(ctx, c, srKey, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(sr.Status.LastFailureDetails).NotTo(BeEmpty())
			detail := sr.Status.LastFailureDetails[0]
			Expect(detail.ContainerName).To(Equal(srName))
			Expect(detail.LastTerminationExitCode).NotTo(BeNil())
			Expect(*detail.LastTerminationExitCode).To(Equal(int32(1)))
			Expect(detail.LastTerminationReason).NotTo(BeEmpty())
			Expect(detail.LastTerminationMessage).To(ContainSubstring("ERROR: connection refused"))
		})

		It("should clear LastFailureDetails when container recovers", func() {
			srName := stack.Spec.ResourceNames[0]
			srKey := client.ObjectKey{Name: srName, Namespace: env.TestNamespace}

			By("Updating StackResource to use a healthy image")
			sr := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: srName, Namespace: stack.Namespace}, sr)).To(Succeed())
			sr.Spec.ImageSpec.Image = "nginx:1.25-alpine"
			sr.Spec.Command = nil
			sr.Spec.Args = nil
			Expect(c.Update(ctx, sr)).To(Succeed())

			By("Waiting for StackResource to become Available")
			sr2, err := helpers.WaitForStackResourceAvailable(ctx, c, srKey, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(sr2.Status.LastFailureDetails).To(BeNil())
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("LastFailureDetails - image pull failure", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.ImagePullFailStack("imgpull-fail")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should populate LastFailureDetails with ImagePullBackOff", func() {
			srName := stack.Spec.ResourceNames[0]
			srKey := client.ObjectKey{Name: srName, Namespace: env.TestNamespace}

			By("Waiting for LastFailureDetails to be populated")
			sr, err := helpers.WaitForLastFailureDetails(ctx, c, srKey, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(sr.Status.LastFailureDetails).NotTo(BeEmpty())
			detail := sr.Status.LastFailureDetails[0]
			Expect(detail.LastTerminationReason).To(BeElementOf("ImagePullBackOff", "ErrImagePull"))
			Expect(detail.LastTerminationExitCode).To(BeNil())
			Expect(detail.LastTerminationMessage).To(ContainSubstring("nonexistent-registry.example.com/fake-image"))
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("LastFailureDetails - init container failure", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.InitContainerFailStack("initfail")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should populate LastFailureDetails with init container crash info", func() {
			srName := stack.Spec.ResourceNames[0]
			srKey := client.ObjectKey{Name: srName, Namespace: env.TestNamespace}

			By("Waiting for LastFailureDetails to be populated")
			sr, err := helpers.WaitForLastFailureDetails(ctx, c, srKey, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(sr.Status.LastFailureDetails).NotTo(BeEmpty())
			detail := sr.Status.LastFailureDetails[0]
			Expect(detail.ContainerName).To(Equal(srName + "-init"))
			Expect(detail.LastTerminationExitCode).NotTo(BeNil())
			Expect(*detail.LastTerminationExitCode).To(Equal(int32(1)))
			Expect(detail.LastTerminationMessage).To(ContainSubstring("FATAL: missing required config"))
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("LastFailureDetails - StatefulService crash", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.NewStack("sts-crash",
				fixtures.NewResource("sts-crash", "sts-crash-app",
					fixtures.WithWorkloadType(corev1alpha1.WorkloadTypeStatefulService),
					fixtures.WithoutPorts(),
					fixtures.WithImage("busybox:1.36"),
					fixtures.WithCommand(
						[]string{"sh"},
						[]string{"-c", "echo 'app starting'; echo 'ERROR: connection refused'; exit 1"})))
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should populate LastFailureDetails when the StatefulSet pod crashes", func() {
			srName := stack.Spec.ResourceNames[0]
			srKey := client.ObjectKey{Name: srName, Namespace: env.TestNamespace}

			By("Waiting for LastFailureDetails to be populated from the crashing StatefulSet pod")
			sr, err := helpers.WaitForLastFailureDetails(ctx, c, srKey, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(sr.Status.LastFailureDetails).NotTo(BeEmpty())
			detail := sr.Status.LastFailureDetails[0]
			Expect(detail.ContainerName).To(Equal(srName))
			Expect(detail.LastTerminationExitCode).NotTo(BeNil())
			Expect(*detail.LastTerminationExitCode).To(Equal(int32(1)))
			Expect(detail.LastTerminationReason).NotTo(BeEmpty())
			Expect(detail.LastTerminationMessage).To(ContainSubstring("ERROR: connection refused"))

			By("Verifying a StatefulSet (not a Deployment) backs the resource")
			_, stsErr := helpers.GetStatefulSetForResource(ctx, c, stack.Namespace, srName)
			Expect(stsErr).NotTo(HaveOccurred())
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, srName)).To(BeFalse())
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("LastFailureDetails - Job crash", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.NewStack("job-crash",
				fixtures.NewResource("job-crash", "job-crash-runner",
					fixtures.WithWorkloadType(corev1alpha1.WorkloadTypeJob),
					fixtures.WithoutPorts(),
					fixtures.WithImage("busybox:1.36"),
					fixtures.WithCommand(
						[]string{"sh"},
						[]string{"-c", "echo 'ERROR: job boom'; exit 1"})))
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		// The Job path captures failure details while the Job is still retrying (its
		// pods crash-loop under OnFailure), so this surfaces quickly rather than only
		// after backoffLimit is exhausted. The terminal LastRunSucceeded=false verdict
		// arrives only after backoffLimit=6 restart backoffs (minutes) and is covered
		// by unit tests instead.
		It("should populate LastFailureDetails from the crash-looping Job pod", func() {
			srName := stack.Spec.ResourceNames[0]
			srKey := client.ObjectKey{Name: srName, Namespace: env.TestNamespace}

			By("Waiting for LastFailureDetails to be populated from the crashing Job pod")
			sr, err := helpers.WaitForLastFailureDetails(ctx, c, srKey, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(sr.Status.LastFailureDetails).NotTo(BeEmpty())
			detail := sr.Status.LastFailureDetails[0]
			Expect(detail.ContainerName).To(Equal(srName))
			Expect(detail.LastTerminationExitCode).NotTo(BeNil())
			Expect(*detail.LastTerminationExitCode).To(Equal(int32(1)))
			Expect(detail.LastTerminationMessage).To(ContainSubstring("job boom"))

			By("Verifying a Job (not a Deployment or Service) backs the resource")
			_, jobErr := helpers.GetJobForResource(ctx, c, stack.Namespace, srName)
			Expect(jobErr).NotTo(HaveOccurred())
			Expect(helpers.DeploymentExists(ctx, c, stack.Namespace, srName)).To(BeFalse())
			_, svcErr := helpers.GetServiceForResource(ctx, c, stack.Namespace, srName)
			Expect(errors.IsNotFound(svcErr)).To(BeTrue())
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})
})
