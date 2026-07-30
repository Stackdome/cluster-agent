package workload

import (
	"context"
	"net"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/pkg/portcheck"
)

var _ = Describe("readinessFailureDetail", func() {
	var detail v1alpha1.LastFailureDetail

	BeforeEach(func() {
		detail = readinessFailureDetail("svc-01", portcheck.Result{Ports: []portcheck.PortResult{
			{Port: 9090, Open: true},
			{Port: 80, Open: false},
		}})
	})

	It("classifies the failure so the hub does not mislabel it a crash", func() {
		Expect(detail.Type).To(Equal(v1alpha1.FailureTypeReadinessFailure))
	})

	It("uses the resource name as the container name", func() {
		// The hub's MapLastFailureDetails discards details whose container name
		// matches neither the resource nor its init container.
		Expect(detail.ContainerName).To(Equal("svc-01"))
	})

	It("names the dead port in a message the user can act on", func() {
		Expect(detail.LastTerminationReason).To(Equal("PortNotListening"))
		Expect(detail.LastTerminationMessage).To(ContainSubstring("80"))
		Expect(detail.LastTerminationMessage).To(ContainSubstring("0.0.0.0"))
	})
})

var _ = Describe("capturePortVerification", func() {
	var (
		reconciler *Reconciler
		resource   *v1alpha1.StackResource
		deadPort   int32
	)

	BeforeEach(func() {
		// A port that was bound and released is the cheapest way to guarantee a
		// connection refusal without waiting for a dial timeout.
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		deadPort = int32(listener.Addr().(*net.TCPAddr).Port)
		Expect(listener.Close()).To(Succeed())

		verifier := portcheck.NewVerifier(1, time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		verifier.Start(ctx)

		reconciler = &Reconciler{PortVerifier: verifier}
		resource = &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-01", Namespace: "team-a"},
			Spec:       v1alpha1.StackResourceSpec{Ports: []v1alpha1.Port{{Name: "http", Number: deadPort}}},
		}
	})

	It("reports a proven-dead port from cache without ever touching the API", func() {
		key := portcheck.Key{Namespace: "team-a", Name: "svc-01", Revision: "7"}
		reconciler.PortVerifier.Enqueue(key, "127.0.0.1", []int32{deadPort})
		Eventually(func() bool {
			_, done := reconciler.PortVerifier.Get(key)
			return done
		}).Should(BeTrue())

		// UncachedClient is nil: a cache hit must not attempt a pod lookup.
		Expect(reconciler.capturePortVerification(context.Background(), resource, "7")).To(BeTrue())
		Expect(resource.Status.LastFailureDetails).To(HaveLen(1))
		Expect(resource.Status.LastFailureDetails[0].Type).To(Equal(v1alpha1.FailureTypeReadinessFailure))
	})

	It("stays silent on a different revision, so a rollout is never judged on a stale answer", func() {
		key := portcheck.Key{Namespace: "team-a", Name: "svc-01", Revision: "7"}
		reconciler.PortVerifier.Enqueue(key, "127.0.0.1", []int32{deadPort})
		Eventually(func() bool {
			_, done := reconciler.PortVerifier.Get(key)
			return done
		}).Should(BeTrue())

		// Revision 8 has no result yet, and with no client there is no pod to find.
		Expect(reconciler.capturePortVerification(context.Background(), resource, "8")).To(BeFalse())
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())
	})

	It("does nothing for a resource that declares no ports", func() {
		resource.Spec.Ports = nil
		Expect(reconciler.capturePortVerification(context.Background(), resource, "7")).To(BeFalse())
	})

	It("is disabled rather than fatal when no verifier is configured", func() {
		bare := &Reconciler{}
		Expect(bare.capturePortVerification(context.Background(), resource, "7")).To(BeFalse())
		Expect(bare.portVerificationAnswered(resource, "7")).To(BeTrue())
	})
})

var _ = Describe("declaredPortNumbers", func() {
	It("returns every declared port, since the kubelet probe guards only one", func() {
		resource := &v1alpha1.StackResource{Spec: v1alpha1.StackResourceSpec{Ports: []v1alpha1.Port{
			{Name: "http", Number: 80},
			{Name: "metrics", Number: 9090},
		}}}

		Expect(declaredPortNumbers(resource)).To(Equal([]int32{80, 9090}))
	})
})
