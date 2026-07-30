package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

// readinessFailureDetail returns the first readiness_failure entry recorded on
// the resource, if any.
func readinessFailureDetail(sr *corev1alpha1.StackResource) (corev1alpha1.LastFailureDetail, bool) {
	for _, detail := range sr.Status.LastFailureDetails {
		if detail.Type == corev1alpha1.FailureTypeReadinessFailure {
			return detail, true
		}
	}
	return corev1alpha1.LastFailureDetail{}, false
}

var _ = Describe("StackResource port readiness", Ordered, func() {
	var (
		brokenStack  *corev1alpha1.Stack
		brokenKey    client.ObjectKey
		healthyStack *corev1alpha1.Stack
	)

	BeforeAll(func() {
		swr := fixtures.StackWithMismatchedPort("port-mismatch")
		Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
		brokenStack = swr.Stack
		brokenKey = client.ObjectKey{
			Name:      brokenStack.Spec.ResourceNames[0],
			Namespace: brokenStack.Namespace,
		}
	})

	// The declared port is 80 but crccheck/hello-world listens on 8000, so the
	// synthesized TCP probe never passes and the port verifier proves the port
	// dead. Polling for the full field-change window covers pod scheduling, the
	// image pull, the async dial, and the reconcile that reads its result.
	It("does not report Available when nothing listens on the declared port", func() {
		By("Polling the StackResource and expecting Available to stay false")
		sr, err := helpers.WaitForStackResourceAvailable(ctx, c, brokenKey, fieldChangeTimeout)
		Expect(err).To(HaveOccurred(),
			"StackResource became Available even though nothing listens on the declared port")
		Expect(helpers.StackResourceIsAvailable(sr)).To(BeFalse())
	})

	It("records a readiness_failure naming the port", func() {
		By("Waiting for a readiness_failure entry in LastFailureDetails")
		sr, err := helpers.WaitFor(ctx, c, brokenKey, &corev1alpha1.StackResource{},
			func(sr *corev1alpha1.StackResource) bool {
				_, found := readinessFailureDetail(sr)
				return found
			}, fieldChangeTimeout)
		Expect(err).NotTo(HaveOccurred())

		detail, found := readinessFailureDetail(sr)
		Expect(found).To(BeTrue())
		Expect(detail.Type).To(Equal(corev1alpha1.FailureTypeReadinessFailure))
		Expect(detail.ContainerName).To(Equal(brokenKey.Name))
		Expect(detail.LastTerminationReason).To(Equal("PortNotListening"))
		Expect(detail.LastTerminationMessage).To(ContainSubstring("80"),
			"the failure message should name the port that nobody is listening on")
	})

	// Guards the gate against blocking healthy workloads: nginx declares and
	// serves :80, so it must still converge normally.
	It("still converges a correctly configured resource", func() {
		swr := fixtures.SimpleStack("port-gate-healthy")
		Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
		healthyStack = swr.Stack

		By("Waiting for the correctly configured StackResource to become Available")
		sr, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
			Name:      healthyStack.Spec.ResourceNames[0],
			Namespace: healthyStack.Namespace,
		}, readyTimeout)
		Expect(err).NotTo(HaveOccurred())
		Expect(helpers.StackResourceIsAvailable(sr)).To(BeTrue())

		_, hasReadinessFailure := readinessFailureDetail(sr)
		Expect(hasReadinessFailure).To(BeFalse(),
			"a resource listening on its declared port should record no readiness failure")
	})

	AfterAll(func() {
		helpers.CleanupStack(ctx, c, brokenStack)
		helpers.CleanupStack(ctx, c, healthyStack)
	})
})
