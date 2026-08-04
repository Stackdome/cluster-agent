package workload

import (
	"context"
	"slices"
	"time"

	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
	"stackdome.io/cluster-agent/pkg/portcheck"
)

const (
	// DefaultPortCheckGrace is how long a closed port keeps being re-dialed
	// before the verdict is believed.
	DefaultPortCheckGrace = 3 * time.Minute

	// portCheckRequeueInterval paces the passes that walk the grace window.
	portCheckRequeueInterval = 30 * time.Second

	// A port that failed on a serving workload is polled, because nothing else
	// watches it. The wait doubles per retry, between these two bounds.
	minPortRetryInterval = 1 * time.Minute
	maxPortRetryInterval = 10 * time.Minute
)

func declaredPortNumbers(resource *v1alpha1.StackResource) []int32 {
	return lo.Map(resource.Spec.Ports, func(p v1alpha1.Port, _ int) int32 { return p.Number })
}

func (r *Reconciler) portCheckGrace() time.Duration {
	if r.PortCheckGrace <= 0 {
		return DefaultPortCheckGrace
	}
	return r.PortCheckGrace
}

// checkPorts runs the port verdict for one status branch and returns how long
// to wait before the next pass, zero when nothing is worth waiting for. Only a
// failed serving workload asks to come back:
//
//   - serving: the pod is Ready, so nothing will announce a port that opens
//     later. Polling is the only way back.
//   - not serving: a pod that turns Ready fires a workload event, which brings
//     the reconcile back for free.
func (r *Reconciler) checkPorts(
	ctx context.Context, resource *v1alpha1.StackResource, revision string, serving bool) time.Duration {
	// A crash detail is the more specific diagnosis, so it wins. Drop the stale
	// verdict too, or the hub reports a port failure for a crashing container.
	if !portDiagnosisAllowed(resource) {
		resource.Status.PortCheck = nil
		return 0
	}
	failed, requeueAfter := r.verifyPorts(ctx, resource, revision)
	if !failed {
		// Safe to clear: the guard proved any detail here is our own.
		resource.Status.LastFailureDetails = nil
		return requeueAfter
	}
	r.reportPortNotListening(ctx, resource, recordPortFailure(resource))
	if serving {
		// A dial is already in flight, so read it before falling back to the
		// slow poll — otherwise every answer is a whole interval stale.
		if requeueAfter > 0 {
			return requeueAfter
		}
		return portRetryAfter(resource.Status.PortCheck, r.portCheckGrace())
	}
	return 0
}

// verifyPorts decides whether a workload declared a port nobody listens on.
// Serving and not-serving workloads run the same check — same mistake either
// way. Never dials inline: it reads what the verifier has answered and lines up
// the next dial.
func (r *Reconciler) verifyPorts(ctx context.Context, resource *v1alpha1.StackResource, revision string) (failed bool, requeueAfter time.Duration) {
	// A nil verifier disables the check (unit tests, reduced deployments).
	if r.PortVerifier == nil || len(resource.Spec.Ports) == 0 {
		resource.Status.PortCheck = nil
		return false, 0
	}
	// Keyed by revision so a rollout invalidates the previous answer.
	key := portcheck.Key{Namespace: resource.Namespace, Name: resource.Name, Revision: revision}

	result, answered := r.PortVerifier.Get(key)
	if !answered {
		podsFound, accepted := r.schedulePortCheck(ctx, resource, key)
		// No answer yet: repeat the verdict on status so the phase does not flip
		// while the dial is in flight.
		failed := portAlreadyFailed(resource.Status.PortCheck, revision, r.portCheckGrace())
		if accepted || podsFound {
			return failed, portCheckRequeueInterval
		}
		return failed, 0
	}

	if result.AllOpen() {
		resource.Status.PortCheck = &v1alpha1.PortCheckStatus{
			Revision: revision,
			Status:   v1alpha1.PortCheckStatusTypeSuccess,
		}
		return false, 0
	}

	clockStartedAt := graceClockStart(resource.Status.PortCheck, revision)
	if clockStartedAt == nil {
		clockStartedAt = ptr.To(metav1.Now())
	}
	resource.Status.PortCheck = &v1alpha1.PortCheckStatus{
		Revision:           revision,
		Status:             v1alpha1.PortCheckStatusTypeFailure,
		FailingPortNumbers: result.ClosedPorts(),
		FailingSince:       clockStartedAt,
	}
	// Drop the answer just judged. Enqueue refuses while one is stored, so the
	// next pass cannot schedule a fresh dial until this is gone.
	r.PortVerifier.Forget(key)

	if time.Since(clockStartedAt.Time) < r.portCheckGrace() {
		return false, portCheckRequeueInterval
	}
	return true, 0
}

// portRetryAfter says how long to wait before dialing a failed port again.
// Waiting the length of the outage doubles the wait on every retry, so no
// attempt counter is needed on status:
//
//	1m, 1m, 2m, 4m, 8m, 10m, 10m...
func portRetryAfter(pc *v1alpha1.PortCheckStatus, grace time.Duration) time.Duration {
	if pc == nil || pc.FailingSince == nil {
		return minPortRetryInterval
	}
	deadFor := time.Since(pc.FailingSince.Time) - grace
	return min(max(deadFor, minPortRetryInterval), maxPortRetryInterval)
}

// portAlreadyFailed reports whether status already holds a failed verdict for
// this revision whose grace period has run out.
func portAlreadyFailed(pc *v1alpha1.PortCheckStatus, revision string, grace time.Duration) bool {
	clockStartedAt := graceClockStart(pc, revision)
	return clockStartedAt != nil &&
		len(pc.FailingPortNumbers) > 0 &&
		time.Since(clockStartedAt.Time) >= grace
}

// recordPortFailure renders the verdict on status into the detail the hub
// shows.
//
//   - Rendered from status, not from the dial, so a repeated verdict reads
//     exactly like a fresh one.
//   - ContainerName must be the resource name or the hub's
//     MapLastFailureDetails drops the detail.
func recordPortFailure(resource *v1alpha1.StackResource) string {
	failing := resource.Status.PortCheck.FailingPortNumbers
	dial := portcheck.Result{Ports: lo.Map(resource.Spec.Ports,
		func(p v1alpha1.Port, _ int) portcheck.PortResult {
			return portcheck.PortResult{Port: p.Number, Open: !slices.Contains(failing, p.Number)}
		})}
	msg := dial.Message()
	resource.Status.LastFailureDetails = []v1alpha1.LastFailureDetail{{
		Type:                   v1alpha1.FailureTypeReadinessFailure,
		ContainerName:          resource.Name,
		LastTerminationReason:  "PortNotListening",
		LastTerminationMessage: msg,
	}}
	return msg
}

// graceClockStart returns when the clock for this revision started, or nil if
// it is not running. A Success record has no FailingSince, so a port that goes
// bad after proving itself open gets a full grace period again.
func graceClockStart(pc *v1alpha1.PortCheckStatus, revision string) *metav1.Time {
	if pc == nil || pc.Revision != revision {
		return nil
	}
	return pc.FailingSince
}

// portDiagnosisAllowed reports whether the port verdict may own
// LastFailureDetails. A crash detail is the more specific diagnosis and keeps it.
func portDiagnosisAllowed(resource *v1alpha1.StackResource) bool {
	details := resource.Status.LastFailureDetails
	return len(details) == 0 || details[0].Type == v1alpha1.FailureTypeReadinessFailure
}

// schedulePortCheck queues a dial and reports whether an answer is coming —
// this dial was admitted, or one for this resource is already in flight. False
// means no pod is running to dial, so there is nothing to come back for.
func (r *Reconciler) schedulePortCheck(
	ctx context.Context, resource *v1alpha1.StackResource, key portcheck.Key) (
	podFound,
	accepted bool,
) {
	podIP, found := r.representativePodIP(ctx, resource, key.Revision)
	if !found {
		return false, false
	}
	return true, r.PortVerifier.Enqueue(key, podIP, declaredPortNumbers(resource)) || r.PortVerifier.Pending(key)
}

func (r *Reconciler) representativePodIP(
	ctx context.Context, resource *v1alpha1.StackResource, revision string) (string, bool) {
	if r.UncachedClient == nil {
		return "", false
	}
	if resource.Spec.WorkloadType == v1alpha1.WorkloadTypeStatefulService {
		return r.statefulSetPodIP(ctx, resource, revision)
	}
	return r.deploymentPodIP(ctx, resource, revision)
}

// Walks deployment revision -> ReplicaSet -> pod, mirroring
// captureLastFailureDetails so both agree on which revision is current.
func (r *Reconciler) deploymentPodIP(ctx context.Context, resource *v1alpha1.StackResource, deploymentRevision string) (string, bool) {
	labels := GetWorkloadLabelForResource(resource)

	replicaSetList := &appsv1.ReplicaSetList{}
	if err := r.UncachedClient.List(ctx, replicaSetList,
		client.InNamespace(resource.Namespace),
		client.MatchingLabels(map[string]string{"resource": labels["resource"]}),
	); err != nil {
		controller.LoggerFromContext(ctx).Error(err, "failed to list replicasets for port verification")
		return "", false
	}

	replicaSet, found := lo.Find(replicaSetList.Items, func(rs appsv1.ReplicaSet) bool {
		return rs.Annotations[deploymentRevisionAnnotation] == deploymentRevision
	})
	if !found {
		return "", false
	}
	podTemplateHash, ok := replicaSet.Labels["pod-template-hash"]
	if !ok {
		return "", false
	}

	podList := &corev1.PodList{}
	if err := r.UncachedClient.List(ctx, podList,
		client.InNamespace(resource.Namespace),
		client.MatchingLabels(replicaSet.Spec.Selector.MatchLabels),
		client.MatchingLabels(map[string]string{"pod-template-hash": podTemplateHash}),
	); err != nil {
		controller.LoggerFromContext(ctx).Error(err, "failed to list pods for port verification")
		return "", false
	}

	return firstDialablePodIP(podList.Items)
}

// StatefulSets have no ReplicaSet; pods carry the controller revision directly.
func (r *Reconciler) statefulSetPodIP(ctx context.Context, resource *v1alpha1.StackResource, controllerRevision string) (string, bool) {
	if controllerRevision == "" {
		return "", false
	}
	podList := &corev1.PodList{}
	if err := r.UncachedClient.List(ctx, podList,
		client.InNamespace(resource.Namespace),
		client.MatchingLabels(GetWorkloadLabelForResource(resource)),
		client.MatchingLabels(map[string]string{"controller-revision-hash": controllerRevision}),
	); err != nil {
		controller.LoggerFromContext(ctx).Error(err, "failed to list statefulset pods for port verification")
		return "", false
	}
	return firstDialablePodIP(podList.Items)
}

// Running with an IP is dialable even if not Ready — exactly the state a
// wrong-port workload sits in.
func firstDialablePodIP(pods []corev1.Pod) (string, bool) {
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			return pod.Status.PodIP, true
		}
	}
	return "", false
}

// reportPortNotListening states the verdict: a declared port stayed closed
// past the grace period. The failure rides WorkloadConverged and the Failed
// verdict, never WorkloadAvailable — each branch already recorded whether the
// workload serves. Derivation turns a serving workload into Phase=Failed /
// Stalled=True / Available=True("ServingButStalled"), and a non-serving one
// into Phase=Failed / Stalled=True / Available=False.
func (r *Reconciler) reportPortNotListening(ctx context.Context, resource *v1alpha1.StackResource, msg string) {
	r.Status.SetCondition(resource, v1alpha1.StackResourceWorkloadConverged, false, "PortNotListening", msg)
	r.Status.ReportFailed(ctx, resource, "PortNotListening", msg)
}
