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

var _ = Describe("capturePortVerificationAfterProbe (grace-bounded condemnation)", func() {
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

		verifier := portcheck.NewVerifier(1, time.Second)
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
		// refusal — the premature verdict this retry exists to survive.
		Expect(reconciler.capturePortVerification(ctx, resource, "7")).To(BeFalse())
		Expect(waitForVerdict().AllOpen()).To(BeFalse())
	})

	It("re-dials a booted app instead of trusting a verdict cached while it was starting", func() {
		// The app finishes booting and binds the port it declared.
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = listener.Close() })

		// First read on the serving path must discard the stale refusal rather
		// than condemn the resource on it.
		Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).To(BeFalse())
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())

		Expect(waitForVerdict().AllOpen()).To(BeTrue())

		// With the port proven open the resource stays available.
		Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).To(BeFalse())
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())
	})

	It("keeps re-verifying a closed verdict while the budget lasts, so a slow secondary port is never condemned", func() {
		// The old policy allowed exactly one retry per revision, which pinned an
		// app whose secondary port bound a few seconds after its primary probe
		// passed at Available=False until the next rollout. Condemnation is a
		// deadline now, so every pass inside the budget re-dials.
		for i := 0; i < 4; i++ {
			Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).
				To(BeFalse(), "pass %d must re-verify rather than condemn", i)
			Expect(resource.Status.LastFailureDetails).To(BeEmpty())
			Expect(waitForVerdict().AllOpen()).To(BeFalse())
		}

		// And the app that finally binds is believed, not stuck behind a spent
		// retry counter.
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = listener.Close() })

		Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).To(BeFalse())
		Expect(waitForVerdict().AllOpen()).To(BeTrue())
		Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).To(BeFalse())
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())
	})

	It("condemns a closed verdict that outlives the budget, so a genuinely dead port is still reported", func() {
		reconciler.PortCheckGrace = time.Nanosecond

		Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).To(BeTrue())
		Expect(resource.Status.LastFailureDetails).To(HaveLen(1))
		Expect(resource.Status.LastFailureDetails[0].Type).To(Equal(v1alpha1.FailureTypeReadinessFailure))
	})

	It("stays condemned once the budget is spent, never churning back to unverified", func() {
		reconciler.PortCheckGrace = time.Nanosecond

		for i := 0; i < 3; i++ {
			Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).To(BeTrue())
			// No further Forget: the cached verdict survives, so the resource
			// cannot oscillate between failed and unverified forever.
			_, done := reconciler.PortVerifier.Get(key)
			Expect(done).To(BeTrue())
		}
	})

	It("clears the budget on an open verdict, so a later flap is judged fresh", func() {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		Expect(err).NotTo(HaveOccurred())

		Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).To(BeFalse())
		Expect(waitForVerdict().AllOpen()).To(BeTrue())

		// Age the budget: this workload has been verified open since long before
		// its grace window would have expired.
		reconciler.portCheckStartedAt[key] = time.Now().Add(-time.Hour)

		Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).To(BeFalse())
		Expect(reconciler.portCheckBudgetActive(key)).To(BeFalse(), "an open verdict must drop the budget")

		// The app dies. The verdict flips closed, and the hour-old stamp must
		// not condemn it on sight.
		Expect(listener.Close()).To(Succeed())
		reconciler.PortVerifier.Forget(key)
		Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).To(BeFalse())
		Expect(waitForVerdict().AllOpen()).To(BeFalse())
		Expect(reconciler.capturePortVerificationAfterProbe(ctx, resource, "7")).
			To(BeFalse(), "the flap must start a fresh budget, not inherit the spent one")
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
	)

	awaitVerdict := func() {
		Eventually(func() bool {
			_, done := reconciler.PortVerifier.Get(key)
			return done
		}, "5s", "10ms").Should(BeTrue())
	}

	BeforeEach(func() {
		ctx = context.Background()

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		port = int32(listener.Addr().(*net.TCPAddr).Port)
		Expect(listener.Close()).To(Succeed())

		verifier := portcheck.NewVerifier(1, time.Second)
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

	It("drives Converged False alongside Available once a closed verdict outlives the budget", func() {
		// Pass 1 schedules the check, pass 2 reads the refusal and — still inside
		// the budget — re-dials rather than judging the resource. Both requeue.
		Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
			To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)))
		awaitVerdict()
		Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
			To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)))
		awaitVerdict()

		// The budget runs out with the port still closed. Only now is the verdict
		// believed.
		reconciler.PortCheckGrace = time.Nanosecond
		result := reconciler.evaluateDeploymentStatus(ctx, resource, deployment)

		Expect(result).To(Equal(controller.ResultStop))
		available := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceWorkloadAvailable))
		Expect(available).NotTo(BeNil())
		Expect(available.Status).To(Equal(metav1.ConditionFalse))
		Expect(available.Reason).To(Equal("PortNotListening"))

		// A resource nobody can reach must not also read "fully converged".
		converged := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceConverged))
		Expect(converged).NotTo(BeNil())
		Expect(converged.Status).To(Equal(metav1.ConditionFalse))
		Expect(converged.Reason).To(Equal("PortNotListening"))
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

		// Prime a closed verdict whose budget has already run out, so nothing but
		// the guard can be keeping the crash detail alive.
		reconciler.PortVerifier.Enqueue(key, "127.0.0.1", []int32{port})
		awaitVerdict()
		reconciler.PortCheckGrace = time.Nanosecond

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
// then re-discovers is, by definition, long settled — so anchoring the "is it
// worth coming back to read the answer" decision on rollout settledness or on
// the convergence timestamp meant the freshly scheduled check was never read.
// The result sat in the cache until some unrelated event or the 10h resync
// happened to reconcile the resource again.
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
			PortVerifier: portcheck.NewVerifier(1, time.Second),
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

	It("stops requeueing once the budget is spent, so an unanswerable check cannot poll forever", func() {
		reconciler.UncachedClient = newStatefulSetPortCheckClient("rev-1")
		reconciler.PortCheckGrace = time.Nanosecond
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
