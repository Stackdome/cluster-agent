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

var _ = Describe("recordPortFailure", func() {
	var detail v1alpha1.LastFailureDetail

	BeforeEach(func() {
		resource := &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-01"},
			Spec: v1alpha1.StackResourceSpec{
				Ports: []v1alpha1.Port{{Number: 9090}, {Number: 80}},
			},
			Status: v1alpha1.StackResourceStatus{
				PortCheck: &v1alpha1.PortCheckStatus{FailingPortNumbers: []int32{80}},
			},
		}
		recordPortFailure(resource)
		Expect(resource.Status.LastFailureDetails).To(HaveLen(1))
		detail = resource.Status.LastFailureDetails[0]
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

var _ = Describe("portRetryAfter", func() {
	// Only the serving path polls a failed port, and the wait it gets is the
	// length of the outage so far.
	retryAfter := func(deadFor time.Duration) time.Duration {
		return portRetryAfter(&v1alpha1.PortCheckStatus{
			FailingSince: ptr.To(metav1.NewTime(time.Now().Add(-DefaultPortCheckGrace - deadFor))),
		}, DefaultPortCheckGrace)
	}

	It("waits the minimum right after the port first fails", func() {
		Expect(retryAfter(0)).To(Equal(minPortRetryInterval))
	})

	It("doubles the wait on every retry", func() {
		// Serving a wait of d puts the next pass at deadFor 2d, which gets 2d
		// back. That is the whole backoff.
		for deadFor := 2 * time.Minute; deadFor <= 8*time.Minute; deadFor *= 2 {
			Expect(retryAfter(deadFor)).To(BeNumerically("~", deadFor, time.Second))
		}
	})

	It("stops growing at the maximum", func() {
		Expect(retryAfter(2 * time.Hour)).To(Equal(maxPortRetryInterval))
	})

	It("waits the minimum when no clock is running", func() {
		Expect(portRetryAfter(nil, DefaultPortCheckGrace)).To(Equal(minPortRetryInterval))
	})
})

var _ = Describe("verifyPorts", func() {
	var (
		reconciler *Reconciler
		resource   *v1alpha1.StackResource
		key        portcheck.Key
		port       int32
		ctx        context.Context
	)

	// waitForDial blocks until the verifier has an answer for key.
	waitForDial := func() portcheck.Result {
		var result portcheck.Result
		Eventually(func() bool {
			res, done := reconciler.PortVerifier.Get(key)
			result = res
			return done
		}, "5s", "10ms").Should(BeTrue())
		return result
	}

	// scheduleDial runs a pass and waits for the dial it lines up, so the next
	// pass has an answer to read.
	scheduleDial := func() {
		reconciler.verifyPorts(ctx, resource, "7")
		waitForDial()
	}

	// spendTheClock backdates the clock so the next refusal is past grace.
	spendTheClock := func() {
		Expect(resource.Status.PortCheck).NotTo(BeNil())
		resource.Status.PortCheck.FailingSince = ptr.To(metav1.NewTime(time.Now().Add(-time.Hour)))
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
	})

	It("starts the clock on the first refusal instead of failing right away", func() {
		scheduleDial()

		failed, requeueAfter := reconciler.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(requeueAfter).To(Equal(portCheckRequeueInterval))
		// The clock lives on status, so it survives an operator restart.
		Expect(resource.Status.PortCheck.Revision).To(Equal("7"))
		Expect(resource.Status.PortCheck.Status).To(Equal(v1alpha1.PortCheckStatusTypeFailure))
		Expect(resource.Status.PortCheck.FailingPortNumbers).To(Equal([]int32{port}))
		Expect(resource.Status.PortCheck.FailingSince).NotTo(BeNil())
	})

	It("keeps the same clock across passes, so the grace period can actually end", func() {
		var startedAt time.Time
		for i := range 3 {
			scheduleDial()
			failed, _ := reconciler.verifyPorts(ctx, resource, "7")
			Expect(failed).To(BeFalse(), "pass %d must re-dial rather than report failure", i)
			if i == 0 {
				startedAt = resource.Status.PortCheck.FailingSince.Time
				continue
			}
			Expect(resource.Status.PortCheck.FailingSince.Time).To(Equal(startedAt))
		}
	})

	It("reports failure for a port that is still closed once the clock outlives the grace period", func() {
		scheduleDial()
		_, _ = reconciler.verifyPorts(ctx, resource, "7")
		spendTheClock()

		scheduleDial()
		failed, requeueAfter := reconciler.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeTrue())
		Expect(requeueAfter).To(BeZero(), "the caller decides whether to poll a failed port")
		Expect(resource.Status.PortCheck.FailingPortNumbers).To(Equal([]int32{port}))
	})

	It("repeats the verdict while the next dial is in flight, so the phase never flips back", func() {
		scheduleDial()
		_, _ = reconciler.verifyPorts(ctx, resource, "7")
		spendTheClock()
		scheduleDial()
		_, _ = reconciler.verifyPorts(ctx, resource, "7")

		// This pass has no answer to read: it only schedules the next dial. The
		// verdict must come off status unchanged.
		failed, requeueAfter := reconciler.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeTrue())
		Expect(requeueAfter).To(Equal(portCheckRequeueInterval))
	})

	It("clears the failure when the port finally opens, without waiting for a new revision", func() {
		scheduleDial()
		_, _ = reconciler.verifyPorts(ctx, resource, "7")
		spendTheClock()
		scheduleDial()
		failed, _ := reconciler.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeTrue())

		// The app binds the port it declared.
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = listener.Close() })

		// One pass to dial, one to read it.
		failed, _ = reconciler.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeTrue(), "the verdict holds until a dial says otherwise")
		Expect(waitForDial().AllOpen()).To(BeTrue())

		failed, requeueAfter := reconciler.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(requeueAfter).To(BeZero())
		Expect(resource.Status.PortCheck.Status).To(Equal(v1alpha1.PortCheckStatusTypeSuccess))
		Expect(resource.Status.PortCheck.FailingSince).To(BeNil(), "an open port stops the clock")
	})

	It("drops the port detail once the ports pass, so a recovered workload stops reporting one", func() {
		// checkPorts owns LastFailureDetails; verifyPorts only writes the verdict.
		resource.Status.LastFailureDetails = []v1alpha1.LastFailureDetail{{
			Type:          v1alpha1.FailureTypeReadinessFailure,
			ContainerName: "svc-01",
		}}
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = listener.Close() })

		scheduleDial()
		Expect(reconciler.checkPorts(ctx, resource, "7", true)).To(BeZero())
		Expect(resource.Status.LastFailureDetails).To(BeEmpty())
	})

	It("gives a new revision its own clock", func() {
		// The old revision's clock is long spent. Revision 7 must not inherit it.
		resource.Status.PortCheck = &v1alpha1.PortCheckStatus{
			Revision:           "6",
			Status:             v1alpha1.PortCheckStatusTypeFailure,
			FailingPortNumbers: []int32{port},
			FailingSince:       ptr.To(metav1.NewTime(time.Now().Add(-time.Hour))),
		}

		scheduleDial()
		failed, _ := reconciler.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(resource.Status.PortCheck.Revision).To(Equal("7"))
		Expect(resource.Status.PortCheck.FailingSince.Time).To(BeTemporally("~", time.Now(), time.Minute))
	})

	It("starts a new clock when a port that proved itself open goes bad again", func() {
		// A Success record carries no FailingSince, so the next refusal on the
		// same revision must start a clock rather than read one that isn't there.
		resource.Status.PortCheck = &v1alpha1.PortCheckStatus{
			Revision: "7",
			Status:   v1alpha1.PortCheckStatusTypeSuccess,
		}

		scheduleDial()
		failed, _ := reconciler.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse(), "a fresh clock must not be spent on sight")
		Expect(resource.Status.PortCheck.Status).To(Equal(v1alpha1.PortCheckStatusTypeFailure))
		Expect(resource.Status.PortCheck.FailingSince).NotTo(BeNil())
	})

	It("does not wait when nothing is dialable, so an unanswerable check costs no reconciles", func() {
		reconciler.UncachedClient = nil

		failed, requeueAfter := reconciler.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(requeueAfter).To(BeZero(), "a dial that was never scheduled is not worth coming back for")
	})

	It("does nothing for a resource that declares no ports", func() {
		resource.Spec.Ports = nil

		failed, requeueAfter := reconciler.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(requeueAfter).To(BeZero())
		Expect(resource.Status.PortCheck).To(BeNil())
	})

	It("is disabled rather than fatal when no verifier is configured", func() {
		bare := &Reconciler{}

		failed, requeueAfter := bare.verifyPorts(ctx, resource, "7")
		Expect(failed).To(BeFalse())
		Expect(requeueAfter).To(BeZero())
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

	It("drives Converged False and a Failed verdict once the clock runs out, and keeps polling for a late bind", func() {
		// Pass 1 schedules the dial, pass 2 reads the refusal and — still inside
		// the grace period — starts the clock. Both come back in 10s.
		Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
			To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)))
		awaitVerdict()
		Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
			To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)))

		// The clock runs out with the port still closed. Pass 3 has no answer to
		// read — pass 2 dropped it — so it repeats the verdict and comes back for
		// the dial it just scheduled.
		resource.Status.PortCheck.FailingSince = ptr.To(metav1.NewTime(time.Now().Add(-time.Hour)))
		Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
			To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)))
		awaitVerdict()

		// Pass 4 judges that answer. The chain keeps running (deferred requeue,
		// not a stop) and the port drops to a slow poll, because a Ready pod
		// produces no event when it finally binds the port.
		result := reconciler.evaluateDeploymentStatus(ctx, resource, deployment)
		Expect(result).To(Equal(controller.ResultDeferredRequeue(maxPortRetryInterval)))

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

	It("alternates the slow poll with a short pass, so an answer is never a poll interval stale", func() {
		// Drive it to the reported-failure state.
		Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
			To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)))
		awaitVerdict()
		reconciler.evaluateDeploymentStatus(ctx, resource, deployment)
		resource.Status.PortCheck.FailingSince = ptr.To(metav1.NewTime(time.Now().Add(-time.Hour)))

		// A judging pass drops the answer it read, so the pass after it always
		// has a dial in flight and must come back for it rather than sleep out
		// the poll — otherwise every answer read is a whole interval old.
		for range 2 {
			Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
				To(Equal(controller.ResultDeferredRequeue(portCheckRequeueInterval)),
					"a dial is in flight")
			awaitVerdict()
			Expect(reconciler.evaluateDeploymentStatus(ctx, resource, deployment)).
				To(Equal(controller.ResultDeferredRequeue(maxPortRetryInterval)),
					"answer judged, back to the slow poll")
		}
		Expect(verdicts.Failed()).NotTo(BeNil())
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

// A pod that is Running and never passes its readiness probe changes nothing on
// the StatefulSet, so no watch event is coming. Without a requeue of its own the
// resource sits Pending forever — which is what a workload declaring the wrong
// port used to do.
var _ = Describe("evaluateStatefulSetStatus port verification while not serving", func() {
	var (
		reconciler *Reconciler
		resource   *v1alpha1.StackResource
		sts        *appsv1.StatefulSet
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
			UncachedClient: newStatefulSetPortCheckClient("rev-1"),
			Status:         testStatusReporter{},
		}
		resource = &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-01", Namespace: "team-a"},
			Spec: v1alpha1.StackResourceSpec{
				WorkloadType: v1alpha1.WorkloadTypeStatefulService,
				Ports:        []v1alpha1.Port{{Name: "http", Number: port}},
			},
		}
		key = portcheck.Key{Namespace: "team-a", Name: "svc-01", Revision: "rev-1"}

		// The pod is Running but not Ready: the declared port is refusing.
		sts = &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-01", Namespace: "team-a", Generation: 1},
			Status: appsv1.StatefulSetStatus{
				ObservedGeneration: 1,
				CurrentRevision:    "rev-1",
				UpdateRevision:     "rev-1",
				Replicas:           1,
			},
		}
	})

	It("keeps requeueing while the clock runs, then reports the failure", func() {
		// Pass 1 schedules the dial, pass 2 reads the refusal and starts the
		// clock. Both requeue: nothing else is going to wake this resource.
		Expect(reconciler.evaluateStatefulSetStatus(ctx, resource, sts)).
			To(Equal(controller.ResultRequeueAfter(portCheckRequeueInterval)))
		awaitVerdict()
		Expect(reconciler.evaluateStatefulSetStatus(ctx, resource, sts)).
			To(Equal(controller.ResultRequeueAfter(portCheckRequeueInterval)))
		Expect(resource.Status.PortCheck.FailingSince).NotTo(BeNil())
		Expect(verdicts.Failed()).To(BeNil(), "still inside the grace period")

		// The clock runs out with the port still closed.
		resource.Status.PortCheck.FailingSince = ptr.To(metav1.NewTime(time.Now().Add(-time.Hour)))
		ctx, verdicts = testCtx()
		reconciler.evaluateStatefulSetStatus(ctx, resource, sts)

		// Failed, not Pending — the hub gets a resource_failed out of this.
		Expect(verdicts.Failed()).NotTo(BeNil())
		Expect(verdicts.Failed().Reason).To(Equal("PortNotListening"))
		Expect(resource.Status.LastFailureDetails).To(HaveLen(1))
		Expect(resource.Status.LastFailureDetails[0].LastTerminationMessage).To(ContainSubstring(fmt.Sprint(port)))
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
