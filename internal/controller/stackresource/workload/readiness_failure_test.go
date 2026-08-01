package workload

import (
	"context"
	"fmt"
	"net"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
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

var _ = Describe("capturePortDiagnosisForNotServingWorkload", func() {
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

		verifier := portcheck.NewVerifierWithDefaults()
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
		reconciler.capturePortDiagnosisForNotServingWorkload(context.Background(), resource, "7")
		Expect(resource.Status.LastFailureDetails).To(HaveLen(1))
		Expect(resource.Status.LastFailureDetails[0].Type).To(Equal(v1alpha1.FailureTypeReadinessFailure))
	})

	It("reports the dial without opening the grace window, and writes the same record every pass", func() {
		key := portcheck.Key{Namespace: "team-a", Name: "svc-01", Revision: "7"}
		reconciler.PortVerifier.Enqueue(key, "127.0.0.1", []int32{deadPort})
		Eventually(func() bool {
			_, done := reconciler.PortVerifier.Get(key)
			return done
		}).Should(BeTrue())

		reconciler.capturePortDiagnosisForNotServingWorkload(context.Background(), resource, "7")
		Expect(resource.Status.PortCheck.Status).To(Equal(v1alpha1.PortCheckStatusTypeFailure))
		Expect(resource.Status.PortCheck.FailingPortNumbers).To(Equal([]int32{deadPort}))
		// A pod that has not started listening yet must not burn the window the
		// serving path depends on.
		Expect(resource.Status.PortCheck.FailingSince).To(BeNil())

		// Every pass must write the same record, or StatusHash changes and the hub
		// sees a new event every reconcile with nothing different.
		hash := resource.StatusHash()
		for range 3 {
			reconciler.capturePortDiagnosisForNotServingWorkload(context.Background(), resource, "7")
			Expect(resource.StatusHash()).To(Equal(hash))
		}
	})

	It("stays silent on a different revision, so a rollout is never judged on a stale answer", func() {
		key := portcheck.Key{Namespace: "team-a", Name: "svc-01", Revision: "7"}
		reconciler.PortVerifier.Enqueue(key, "127.0.0.1", []int32{deadPort})
		Eventually(func() bool {
			_, done := reconciler.PortVerifier.Get(key)
			return done
		}).Should(BeTrue())

		// Revision 8 has no result yet, and with no client there is no pod to find.
		reconciler.capturePortDiagnosisForNotServingWorkload(context.Background(), resource, "8")
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())
	})

	It("does nothing for a resource that declares no ports", func() {
		resource.Spec.Ports = nil
		reconciler.capturePortDiagnosisForNotServingWorkload(context.Background(), resource, "7")
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())
	})

	It("is disabled rather than fatal when no verifier is configured", func() {
		bare := &Reconciler{}
		bare.capturePortDiagnosisForNotServingWorkload(context.Background(), resource, "7")
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())
	})
})

// newPortCheckUncachedClient serves the ReplicaSet and Running pod that the
// port checker walks to find a dialable address for deployment revision 7. The
// pod reports 127.0.0.1 so the dial lands on the spec's own listener.
func newPortCheckUncachedClient() client.Client {
	scheme := runtime.NewScheme()
	Expect(appsv1.AddToScheme(scheme)).To(Succeed())
	Expect(corev1.AddToScheme(scheme)).To(Succeed())
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "svc-01-abc",
				Namespace:   "team-a",
				Labels:      map[string]string{"resource": "svc-01", "pod-template-hash": "abc"},
				Annotations: map[string]string{deploymentRevisionAnnotation: "7"},
			},
			Spec: appsv1.ReplicaSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"resource": "svc-01"}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "svc-01-abc-xyz",
				Namespace: "team-a",
				Labels:    map[string]string{"resource": "svc-01", "pod-template-hash": "abc"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "127.0.0.1"},
		},
	).Build()
}

var _ = Describe("verifyServingPorts (grace-bounded condemnation)", func() {
	var (
		reconciler *Reconciler
		resource   *v1alpha1.StackResource
		key        portcheck.Key
		port       int32
		ctx        context.Context
	)

	// waitForVerdict blocks until the verifier has an answer for key.
	waitForVerdict := func() portcheck.Result {
		var result portcheck.Result
		Eventually(func() bool {
			res, done := reconciler.PortVerifier.Get(key)
			result = res
			return done
		}, "5s", "10ms").Should(BeTrue())
		return result
	}

	BeforeEach(func() {
		ctx = context.Background()

		// Bind and release a port so the first dial is refused, exactly as it
		// would be against an app that has started but not yet bound.
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		port = int32(listener.Addr().(*net.TCPAddr).Port)
		Expect(listener.Close()).To(Succeed())

		verifier := portcheck.NewVerifierWithDefaults()
		verifierCtx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		verifier.Start(verifierCtx)

		reconciler = &Reconciler{PortVerifier: verifier, UncachedClient: newPortCheckUncachedClient()}
		resource = &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-01", Namespace: "team-a"},
			Spec:       v1alpha1.StackResourceSpec{Ports: []v1alpha1.Port{{Name: "http", Number: port}}},
		}
		key = portcheck.Key{Namespace: "team-a", Name: "svc-01", Revision: "7"}

		// The not-serving path dials while the app is still booting and caches a
		// refusal — the premature verdict the grace window exists to survive.
		reconciler.capturePortDiagnosisForNotServingWorkload(ctx, resource, "7")
		Expect(waitForVerdict().AllOpen()).To(BeFalse())
		resource.Status.LastFailureDetails = nil
	})

	It("re-dials a booted app instead of trusting a verdict cached while it was starting", func() {
		// The app finishes booting and binds the port it declared.
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = listener.Close() })

		// First read on the serving path must discard the stale refusal rather
		// than condemn the resource on it.
		failed, outstanding := reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(outstanding).To(BeTrue())
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())
		// The grace window opens at the first serving refusal — and on status, so
		// it survives an operator restart.
		Expect(resource.Status.PortCheck).NotTo(BeNil())
		Expect(resource.Status.PortCheck.Revision).To(Equal("7"))

		Expect(waitForVerdict().AllOpen()).To(BeTrue())

		// With the port proven open the resource stays available and the window
		// is cleared, so a later flap is judged fresh.
		failed, outstanding = reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(outstanding).To(BeFalse())
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())
		Expect(resource.Status.PortCheck.Status).To(Equal(v1alpha1.PortCheckStatusTypeSuccess))
		Expect(resource.Status.PortCheck.FailingSince).To(BeNil(), "a Success closes the window")
	})

	It("keeps re-verifying a closed verdict while the grace window lasts, so a slow secondary port is never condemned", func() {
		firstSeen := time.Time{}
		for i := range 4 {
			failed, outstanding := reconciler.verifyServingPorts(ctx, resource, "7")
			Expect(failed).To(BeFalse(), "pass %d must re-verify rather than condemn", i)
			Expect(outstanding).To(BeTrue())
			Expect(resource.Status.LastFailureDetails).To(BeEmpty())
			// The anchor latches: later passes must not push it forward, or the
			// window would never close.
			if i == 0 {
				firstSeen = resource.Status.PortCheck.FailingSince.Time
			} else {
				Expect(resource.Status.PortCheck.FailingSince.Time).To(Equal(firstSeen))
			}
			Expect(waitForVerdict().AllOpen()).To(BeFalse())
		}

		// And the app that finally binds is believed.
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = listener.Close() })

		failed, outstanding := reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(outstanding).To(BeTrue())
		Expect(waitForVerdict().AllOpen()).To(BeTrue())
		failed, _ = reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())
	})

	It("condemns a closed verdict that outlives the grace window, so a genuinely dead port is still reported", func() {
		resource.Status.PortCheck = &v1alpha1.PortCheckStatus{
			Revision:     "7",
			Status:       v1alpha1.PortCheckStatusTypeFailure,
			FailingSince: ptr.To(metav1.NewTime(time.Now().Add(-time.Hour))),
		}

		failed, outstanding := reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeTrue())
		Expect(outstanding).To(BeFalse())
		Expect(resource.Status.LastFailureDetails).To(HaveLen(1))
		Expect(resource.Status.LastFailureDetails[0].Type).To(Equal(v1alpha1.FailureTypeReadinessFailure))
	})

	It("survives an operator restart mid-window, because the anchor lives on status", func() {
		// The previous operator opened the window an hour ago; this process
		// starts with an empty verifier cache but inherits the status.
		resource.Status.PortCheck = &v1alpha1.PortCheckStatus{
			Revision:     "7",
			Status:       v1alpha1.PortCheckStatusTypeFailure,
			FailingSince: ptr.To(metav1.NewTime(time.Now().Add(-time.Hour))),
		}
		freshVerifier := portcheck.NewVerifierWithDefaults()
		verifierCtx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		freshVerifier.Start(verifierCtx)
		reconciler.PortVerifier = freshVerifier

		// First pass schedules a dial and requeues; the window is not reset.
		failed, outstanding := reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(outstanding).To(BeTrue())
		Expect(waitForVerdict().AllOpen()).To(BeFalse())

		// The refusal lands against the inherited, already-spent window.
		failed, _ = reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeTrue())
	})

	It("stays condemned once the window is spent, never churning back to unverified", func() {
		resource.Status.PortCheck = &v1alpha1.PortCheckStatus{
			Revision:     "7",
			Status:       v1alpha1.PortCheckStatusTypeFailure,
			FailingSince: ptr.To(metav1.NewTime(time.Now().Add(-time.Hour))),
		}
		for range 3 {
			failed, outstanding := reconciler.verifyServingPorts(ctx, resource, "7")
			Expect(failed).To(BeTrue())
			Expect(outstanding).To(BeFalse())
			// No further Forget: the cached verdict survives, so the resource
			// cannot oscillate between failed and unverified forever.
			_, done := reconciler.PortVerifier.Get(key)
			Expect(done).To(BeTrue())
		}
	})

	It("judges a new rollout fresh instead of inheriting the old revision's window", func() {
		resource.Status.PortCheck = &v1alpha1.PortCheckStatus{
			Revision:     "6",
			Status:       v1alpha1.PortCheckStatusTypeFailure,
			FailingSince: ptr.To(metav1.NewTime(time.Now().Add(-time.Hour))),
		}

		failed, outstanding := reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse(), "revision 7 must open its own window, not be condemned on revision 6's")
		Expect(outstanding).To(BeTrue())
		Expect(resource.Status.PortCheck.Revision).To(Equal("7"))
	})

	It("clears the window on an open verdict, so a later flap is judged fresh", func() {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		Expect(err).NotTo(HaveOccurred())

		failed, outstanding := reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(outstanding).To(BeTrue())
		Expect(waitForVerdict().AllOpen()).To(BeTrue())

		failed, _ = reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(resource.Status.PortCheck.Status).To(Equal(v1alpha1.PortCheckStatusTypeSuccess))
		Expect(resource.Status.PortCheck.FailingSince).To(BeNil(), "an open verdict must clear the window")

		// The app dies. The verdict flips closed, and the flap must open a fresh
		// window rather than being condemned on sight.
		Expect(listener.Close()).To(Succeed())
		reconciler.PortVerifier.Forget(key)
		failed, _ = reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(waitForVerdict().AllOpen()).To(BeFalse())
		failed, outstanding = reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse(), "the flap must start a fresh window, not inherit the spent one")
		Expect(outstanding).To(BeTrue())
	})

	It("opens a fresh window when a verified-open port goes bad on the same revision", func() {
		// A Success record carries no FailingSince. The next Failure on that same
		// revision must open a new window rather than read through the one that
		// isn't there.
		resource.Status.PortCheck = &v1alpha1.PortCheckStatus{
			Revision: "7",
			Status:   v1alpha1.PortCheckStatusTypeSuccess,
		}
		reconciler.PortVerifier.Forget(key)

		failed, outstanding := reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(outstanding).To(BeTrue())
		Expect(waitForVerdict().AllOpen()).To(BeFalse())

		failed, outstanding = reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse(), "a fresh window must not be spent on sight")
		Expect(outstanding).To(BeTrue())
		Expect(resource.Status.PortCheck.Status).To(Equal(v1alpha1.PortCheckStatusTypeFailure))
		Expect(resource.Status.PortCheck.FailingPortNumbers).To(Equal([]int32{port}))
		Expect(resource.Status.PortCheck.FailingSince).NotTo(BeNil())
	})

	It("does not poll when nothing is dialable, so an unanswerable check costs no reconciles", func() {
		reconciler.PortVerifier.Forget(key)
		reconciler.UncachedClient = nil

		failed, outstanding := reconciler.verifyServingPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(outstanding).To(BeFalse(), "a check that was never enqueued is not worth requeueing for")
	})
})

var _ = Describe("evaluateDeploymentStatus port verification", func() {
	var (
		reconciler *Reconciler
		resource   *v1alpha1.StackResource
		deployment *appsv1.Deployment
		key        portcheck.Key
		port       int32
		ctx        context.Context
		verdicts   *controller.VerdictCollector
	)

	awaitVerdict := func() {
		Eventually(func() bool {
			_, done := reconciler.PortVerifier.Get(key)
			return done
		}, "5s", "10ms").Should(BeTrue())
	}

	BeforeEach(func() {
		ctx, verdicts = testCtx()

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		port = int32(listener.Addr().(*net.TCPAddr).Port)
		Expect(listener.Close()).To(Succeed())

		verifier := portcheck.NewVerifierWithDefaults()
		verifierCtx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		verifier.Start(verifierCtx)

		reconciler = &Reconciler{
			PortVerifier:   verifier,
			UncachedClient: newPortCheckUncachedClient(),
			Status:         testStatusReporter{},
		}
		resource = &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-01", Namespace: "team-a"},
			Spec:       v1alpha1.StackResourceSpec{Ports: []v1alpha1.Port{{Name: "http", Number: port}}},
		}
		key = portcheck.Key{Namespace: "team-a", Name: "svc-01", Revision: "7"}

		deployment = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "svc-01",
				Namespace:   "team-a",
				Annotations: map[string]string{deploymentRevisionAnnotation: "7"},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))},
			Status: appsv1.DeploymentStatus{
				Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
				},
			},
		}
	})

	It("drives Converged False and a Failed verdict once a closed verdict outlives the window, and keeps the chain running", func() {
		// Pass 1 schedules the check, pass 2 reads the refusal and — still inside
		// the window — re-dials rather than judging the resource. Both requeue.
		Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
			To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)))
		awaitVerdict()
		Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
			To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)))
		awaitVerdict()

		// The window runs out with the port still closed. Only now is the verdict
		// believed.
		resource.Status.PortCheck.FailingSince = ptr.To(metav1.NewTime(time.Now().Add(-time.Hour)))
		result := reconciler.evaluateDeploymentStatus(ctx, resource, deployment)

		// Condemnation sets conditions but does not stop the chain: the remaining
		// sub-reconcilers (Service, ...) still run.
		Expect(result).To(Equal(controller.ResultContinue))

		// WorkloadAvailable keeps its firsthand truth: the primary port is
		// serving. Only a declared secondary port is closed.
		available := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceWorkloadAvailable))
		Expect(available).NotTo(BeNil())
		Expect(available.Status).To(Equal(metav1.ConditionTrue))

		// A resource with a dead declared port must not read "fully converged".
		converged := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceWorkloadConverged))
		Expect(converged).NotTo(BeNil())
		Expect(converged.Status).To(Equal(metav1.ConditionFalse))
		Expect(converged.Reason).To(Equal("PortNotListening"))

		// Terminal per revision: derivation turns this into Phase=Failed /
		// Stalled=True / Available=True("ServingButStalled").
		Expect(verdicts.Failed()).NotTo(BeNil())
		Expect(verdicts.Failed().Reason).To(Equal("PortNotListening"))
	})

	It("keeps a captured crash detail instead of overwriting it with a port verdict", func() {
		// Serving but not converged, with a crash already diagnosed for this
		// revision — the exit code is the more specific answer and must survive.
		deployment.Status.UnavailableReplicas = 1
		resource.Status.LastFailureDeploymentRevision = "7"
		resource.Status.LastFailureDetails = []v1alpha1.LastFailureDetail{{
			ContainerName:           "svc-01",
			LastTerminationReason:   "OOMKilled",
			LastTerminationExitCode: ptr.To(int32(137)),
		}}

		// Prime a closed verdict whose window has already run out, so nothing but
		// the guard can be keeping the crash detail alive.
		reconciler.PortVerifier.Enqueue(key, "127.0.0.1", []int32{port})
		awaitVerdict()
		resource.Status.PortCheck = &v1alpha1.PortCheckStatus{
			Revision:     "7",
			Status:       v1alpha1.PortCheckStatusTypeFailure,
			FailingSince: ptr.To(metav1.NewTime(time.Now().Add(-time.Hour))),
		}

		reconciler.evaluateDeploymentStatus(ctx, resource, deployment)

		Expect(resource.Status.LastFailureDetails).To(HaveLen(1))
		Expect(resource.Status.LastFailureDetails[0].LastTerminationReason).To(Equal("OOMKilled"))
		Expect(resource.Status.LastFailureDetails[0].LastTerminationExitCode).To(HaveValue(Equal(int32(137))))
	})
})

// newStatefulSetPortCheckClient serves a Running StatefulSet pod on the given
// controller revision, reporting 127.0.0.1 so the dial lands on the spec's own
// listener.
func newStatefulSetPortCheckClient(revision string) client.Client {
	scheme := runtime.NewScheme()
	Expect(appsv1.AddToScheme(scheme)).To(Succeed())
	Expect(corev1.AddToScheme(scheme)).To(Succeed())
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "svc-01-0",
				Namespace: "team-a",
				Labels: map[string]string{
					"resource":                 "svc-01",
					"controller-revision-hash": revision,
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "127.0.0.1"},
		},
	).Build()
}

// An operator restart empties the verifier's in-memory cache. Every workload it
// then re-discovers is, by definition, long settled — so the decision "is it
// worth coming back to read the answer" must not hinge on rollout settledness
// or a convergence timestamp; those suppressed the requeue and left the freshly
// scheduled check unread until some unrelated event or the 10h resync.
var _ = Describe("port verification after an operator restart", func() {
	var (
		reconciler *Reconciler
		resource   *v1alpha1.StackResource
		port       int32
		ctx        context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		port = int32(listener.Addr().(*net.TCPAddr).Port)
		Expect(listener.Close()).To(Succeed())

		// No workers are started: the check stays unanswered for the whole spec,
		// which is exactly the state the requeue has to cover.
		reconciler = &Reconciler{
			PortVerifier: portcheck.NewVerifierWithDefaults(),
			Status:       testStatusReporter{},
		}
		resource = &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-01", Namespace: "team-a"},
			Spec:       v1alpha1.StackResourceSpec{Ports: []v1alpha1.Port{{Name: "http", Number: port}}},
		}
	})

	It("requeues a long-settled Deployment whose check has not been answered", func() {
		reconciler.UncachedClient = newPortCheckUncachedClient()
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "svc-01",
				Namespace:   "team-a",
				Annotations: map[string]string{deploymentRevisionAnnotation: "7"},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))},
			Status: appsv1.DeploymentStatus{
				Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
					{
						Type:               appsv1.DeploymentProgressing,
						Status:             corev1.ConditionTrue,
						Reason:             "NewReplicaSetAvailable",
						LastTransitionTime: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
					},
				},
			},
		}
		// The rollout settled two hours ago, so DeploymentRolloutSettled is true
		// for any grace period — the old gate that suppressed this requeue.
		Expect(controller.DeploymentRolloutSettled(deployment, reconciler.portCheckGrace())).To(BeTrue())

		Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
			To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)))
	})

	It("requeues a converged StatefulSet with no convergence record", func() {
		reconciler.UncachedClient = newStatefulSetPortCheckClient("rev-1")
		// The pod lookup dispatches on workload type: a StatefulSet has no
		// ReplicaSet, so its pods carry the controller revision directly.
		resource.Spec.WorkloadType = v1alpha1.WorkloadTypeStatefulService
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-01", Namespace: "team-a", Generation: 1},
			Status: appsv1.StatefulSetStatus{
				ObservedGeneration: 1,
				CurrentRevision:    "rev-1",
				UpdateRevision:     "rev-1",
				Replicas:           1,
				ReadyReplicas:      1,
				UpdatedReplicas:    1,
				AvailableReplicas:  1,
			},
		}
		// Status.LastConverged is nil, which used to mean "never poll".
		Expect(resource.Status.LastConverged).To(BeNil())

		Expect(reconciler.evaluateStatefulSetStatus(ctx, resource, sts)).
			To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)))
	})

	It("does not requeue when nothing is dialable, so an unanswerable check cannot poll forever", func() {
		// A client with no Running pod: no dial can be scheduled, so there is no
		// answer worth polling for. The workload watch re-triggers when a pod
		// appears.
		resource.Spec.WorkloadType = v1alpha1.WorkloadTypeStatefulService
		reconciler.UncachedClient = newStatefulSetPortCheckClient("some-other-rev")
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-01", Namespace: "team-a", Generation: 1},
			Status: appsv1.StatefulSetStatus{
				ObservedGeneration: 1,
				CurrentRevision:    "rev-1",
				UpdateRevision:     "rev-1",
				Replicas:           1,
				ReadyReplicas:      1,
				UpdatedReplicas:    1,
				AvailableReplicas:  1,
			},
		}

		Expect(reconciler.evaluateStatefulSetStatus(ctx, resource, sts)).To(Equal(controller.ResultContinue))
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
