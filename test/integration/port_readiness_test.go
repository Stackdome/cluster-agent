package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// A pod that never passes its readiness probe produces no further workload
	// events, so this used to sit Pending forever with the diagnosis buried in
	// LastFailureDetails and nothing telling the hub the resource had failed.
	It("reports the resource failed instead of leaving it pending", func() {
		sr, err := helpers.WaitFor(ctx, c, brokenKey, &corev1alpha1.StackResource{},
			func(sr *corev1alpha1.StackResource) bool {
				return sr.Status.Phase == corev1alpha1.StackResourcePhaseFailed
			}, fieldChangeTimeout)
		Expect(err).NotTo(HaveOccurred())

		stalled := meta.FindStatusCondition(sr.Status.Conditions, string(corev1alpha1.StackResourceStalled))
		Expect(stalled).NotTo(BeNil())
		Expect(stalled.Status).To(Equal(metav1.ConditionTrue))
		Expect(stalled.Reason).To(Equal("PortNotListening"))
	})

	// Guards the gate against blocking healthy workloads: nginx declares and
	// serves :80, so it must still converge normally.
	It("still converges a correctly configured resource", func() {
		swr := fixtures.SimpleStack("port-gate-healthy")
		Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
		healthyStack = swr.Stack

		srKey := client.ObjectKey{
			Name:      healthyStack.Spec.ResourceNames[0],
			Namespace: healthyStack.Namespace,
		}

		By("Waiting for the correctly configured StackResource to become Available")
		sr, err := helpers.WaitForStackResourceAvailable(ctx, c, srKey, readyTimeout)
		Expect(err).NotTo(HaveOccurred())
		Expect(helpers.StackResourceIsAvailable(sr)).To(BeTrue())

		// Available=True lands before the port check answers: the first serving
		// reconcile only enqueues the dial, and the result is read on a later
		// reconcile (10s requeue). Asserting on LastFailureDetails the instant
		// Available flips would therefore check before the guarded thing ran.
		// Hold past that window so a gate that wrongly proves a live port dead
		// is caught flipping Available back off.
		By("Holding to confirm the async port check does not later revoke Available")
		Consistently(func(g Gomega) {
			latest, getErr := helpers.GetStackResource(ctx, c, srKey)
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(helpers.StackResourceIsAvailable(latest)).To(BeTrue(),
				"the port readiness gate revoked Available from a resource listening on its declared port")
			_, hasReadinessFailure := readinessFailureDetail(latest)
			g.Expect(hasReadinessFailure).To(BeFalse(),
				"a resource listening on its declared port should record no readiness failure")
		}, stabilityWindow, 5*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		helpers.CleanupStack(ctx, c, brokenStack)
		helpers.CleanupStack(ctx, c, healthyStack)
	})
})
