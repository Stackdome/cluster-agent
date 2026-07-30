package workload

import (
	"context"
	"time"

	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
	"stackdome.io/cluster-agent/pkg/portcheck"
)

const (
	// DefaultPortCheckGrace bounds how long a workload keeps being requeued
	// while its port verification is still outstanding. It matches the
	// deployment rollout grace period, so an unanswerable check costs no more
	// polling than a rollout that never settles.
	DefaultPortCheckGrace = 3 * time.Minute

	// portCheckRequeueInterval is how often an unverified resource comes back to
	// read its result. It mirrors the rollout polling interval already used by
	// the status paths.
	portCheckRequeueInterval = 10 * time.Second
)

// readinessFailureDetail renders a port verification failure into the shared
// LastFailureDetail shape. ContainerName is set to the resource name because
// the hub's MapLastFailureDetails drops details whose container name matches
// neither the resource nor its init container.
func readinessFailureDetail(resourceName string, result portcheck.Result) v1alpha1.LastFailureDetail {
	return v1alpha1.LastFailureDetail{
		Type:                   v1alpha1.FailureTypeReadinessFailure,
		ContainerName:          resourceName,
		LastTerminationReason:  "PortNotListening",
		LastTerminationMessage: result.Message(),
	}
}

// declaredPortNumbers returns the ports the user asked us to publish.
func declaredPortNumbers(resource *v1alpha1.StackResource) []int32 {
	ports := make([]int32, 0, len(resource.Spec.Ports))
	for _, p := range resource.Spec.Ports {
		ports = append(ports, p.Number)
	}
	return ports
}

// portCheckKey scopes a verification to one workload revision, so a rollout
// invalidates the previous answer.
func portCheckKey(resource *v1alpha1.StackResource, revision string) portcheck.Key {
	return portcheck.Key{
		Namespace: resource.Namespace,
		Name:      resource.Name,
		Revision:  revision,
	}
}

// portVerificationApplies reports whether this resource has anything to verify
// with a verifier available to do it. A Reconciler built without a verifier
// (unit tests, workload types that never publish ports) simply skips the check.
func (r *Reconciler) portVerificationApplies(resource *v1alpha1.StackResource) bool {
	return r.PortVerifier != nil && len(resource.Spec.Ports) > 0
}

// portVerificationAnswered reports whether a completed result is already in
// hand. It reads the cache only — no API calls, no dialing — so status paths can
// use it to decide whether another reconcile is worth scheduling.
func (r *Reconciler) portVerificationAnswered(resource *v1alpha1.StackResource, revision string) bool {
	if !r.portVerificationApplies(resource) {
		return true
	}
	_, done := r.PortVerifier.Get(portCheckKey(resource, revision))
	return done
}

// portCheckGrace is the requeue budget for a resource whose verification has
// not come back yet.
func (r *Reconciler) portCheckGrace() time.Duration {
	if r.PortCheckGrace <= 0 {
		return DefaultPortCheckGrace
	}
	return r.PortCheckGrace
}

// beginPortCheckBudget returns the moment key's budget started, opening one at
// now if key has none yet, and re-anchoring an existing budget the first time
// the kubelet probe is known to have passed.
//
// Two rules, for two different failure modes.
//
// The budget is anchored on our own interest rather than on the workload's
// rollout or convergence timestamp because those anchors live on the object and
// survive a process restart while the verifier's cache does not. A long-settled
// workload re-discovered by a freshly started operator would otherwise be born
// with its entire budget already spent — no requeue to read the result it just
// scheduled, no retry for a verdict that arrives late.
//
// It is re-anchored on the first probe-passed sighting because the budget's job
// on that path is to give a slow *secondary* port time to bind, and that clock
// can only start once the primary port is up. A pod that spent longer than the
// whole budget booting — an image pull plus a slow runtime, entirely ordinary
// during the fleet-wide rolling restart this release triggers — would otherwise
// reach the serving branch with nothing left and have its first, premature
// refusal believed on sight. The re-anchor latches, so it cannot be used to
// extend the budget indefinitely.
func (r *Reconciler) beginPortCheckBudget(key portcheck.Key, probePassed bool) time.Time {
	r.portCheckMu.Lock()
	defer r.portCheckMu.Unlock()
	if r.portCheckBudgets == nil {
		r.portCheckBudgets = make(map[portcheck.Key]portCheckBudget)
	}
	budget, ok := r.portCheckBudgets[key]
	switch {
	case !ok:
		budget = portCheckBudget{startedAt: time.Now(), probeAnchored: probePassed}
	case probePassed && !budget.probeAnchored:
		budget = portCheckBudget{startedAt: time.Now(), probeAnchored: true}
	default:
		return budget.startedAt
	}
	r.portCheckBudgets[key] = budget
	return budget.startedAt
}

// portCheckBudgetActive reports, without recording anything, whether key has a
// budget that has not yet run out. A key nobody has asked about has no budget
// and is not worth polling for.
func (r *Reconciler) portCheckBudgetActive(key portcheck.Key) bool {
	r.portCheckMu.Lock()
	defer r.portCheckMu.Unlock()
	budget, ok := r.portCheckBudgets[key]
	if !ok {
		return false
	}
	return time.Since(budget.startedAt) < r.portCheckGrace()
}

// clearPortCheckBudget drops key's budget once its ports verify open, so a
// workload that flaps closed later starts a fresh budget rather than being
// condemned on a stale one.
func (r *Reconciler) clearPortCheckBudget(key portcheck.Key) {
	r.portCheckMu.Lock()
	defer r.portCheckMu.Unlock()
	delete(r.portCheckBudgets, key)
}

// portCheckOutstanding reports whether a verification is still unanswered and
// still inside its budget — that is, whether another reconcile is worth
// scheduling to read the result. It reads the cache only; it never dials and
// never makes an API call.
func (r *Reconciler) portCheckOutstanding(resource *v1alpha1.StackResource, revision string) bool {
	if r.portVerificationAnswered(resource, revision) {
		return false
	}
	return r.portCheckBudgetActive(portCheckKey(resource, revision))
}

// capturePortVerification reports whether a declared port has been proven
// dead, and records a typed failure detail when so.
//
// It is best effort. An unknown result is not a failure: the check is
// scheduled and false is returned, so the caller proceeds normally rather than
// stalling on an answer that may never come. It never dials inline.
//
// The cached result is consulted before any API call, so a resource that has
// already verified costs nothing on subsequent reconciles.
//
// This is the entry point for paths where nothing has yet proven the workload
// is up. A closed verdict recorded here is provisional: see
// capturePortVerificationAfterProbe.
func (r *Reconciler) capturePortVerification(ctx context.Context, resource *v1alpha1.StackResource, deploymentRevision string) bool {
	return r.evaluatePortVerification(ctx, resource, deploymentRevision, false)
}

// capturePortVerificationAfterProbe is the entry point for paths where the
// kubelet probe has already proven the primary port good.
//
// A closed verdict reaching this path is disbelieved for as long as the port
// check budget lasts. A pod gets an IP the moment it starts, so a dial can land
// while a slow-booting app is still binding its listeners; a single premature
// refusal must never become the resource's permanent truth. Condemnation is a
// deadline, not a counter: the verdict has to stay closed past the grace budget
// before it is believed, so an app whose secondary port binds a minute after
// its primary one heals itself instead of staying pinned Available=False until
// the next rollout.
func (r *Reconciler) capturePortVerificationAfterProbe(ctx context.Context, resource *v1alpha1.StackResource, deploymentRevision string) bool {
	return r.evaluatePortVerification(ctx, resource, deploymentRevision, true)
}

func (r *Reconciler) evaluatePortVerification(ctx context.Context, resource *v1alpha1.StackResource, revision string, probePassed bool) bool {
	if !r.portVerificationApplies(resource) {
		return false
	}
	// Keyed by deployment revision rather than pod-template-hash: the revision
	// is already in hand, so a cache hit needs no pod lookup at all, and a new
	// rollout still invalidates the previous answer.
	key := portCheckKey(resource, revision)

	result, done := r.PortVerifier.Get(key)
	if !done {
		// The budget opens only once a dial is actually in flight. A revision
		// with no dialable pod yet — still pulling its image, still scheduling —
		// has nothing to spend a budget on, and spending it there would leave a
		// slow-starting workload with none by the time it is worth checking.
		//
		// Opening it here rather than at the first closed verdict is what makes
		// the caller's requeue possible: portCheckOutstanding polls only keys
		// that have a budget.
		if r.schedulePortCheck(ctx, resource, key) {
			r.beginPortCheckBudget(key, probePassed)
		}
		return false
	}
	if result.AllOpen() {
		r.clearPortCheckBudget(key)
		return false
	}
	if probePassed && time.Since(r.beginPortCheckBudget(key, true)) < r.portCheckGrace() {
		// Still inside the budget: discard the verdict and redial against the
		// workload the probe has since proven up. The answer lands
		// asynchronously; until it does the resource is treated as unverified,
		// so the caller requeues rather than acting on either verdict.
		//
		// The budget is what stops this looping. Each pass costs one dial and
		// one requeue at portCheckRequeueInterval, and once the budget is spent
		// the branch below takes over permanently for this revision.
		r.PortVerifier.Forget(key)
		if !r.schedulePortCheck(ctx, resource, key) {
			// The verdict is already discarded, so the key stays unanswered and
			// the caller keeps requeueing for the rest of the budget. Worth a
			// breadcrumb: repeated occurrences mean the pod listing and the
			// revision disagree about which pods are current.
			controller.LoggerFromContext(ctx).V(1).Info("no dialable pod to re-verify declared ports against",
				"resource", resource.Name, "revision", key.Revision)
		}
		return false
	}
	// The budget entry is deliberately left in place: clearing it here would
	// hand the next reconcile a fresh budget and churn between "condemned" and
	// "re-verifying" forever. An open verdict is the only thing that clears it.
	resource.Status.LastFailureDetails = []v1alpha1.LastFailureDetail{
		readinessFailureDetail(resource.Name, result),
	}
	return true
}

// schedulePortCheck queues a check for key and reports whether a dialable pod
// was found to check. Only a cache miss (or a discarded verdict) reaches here,
// so the pod lookup is never on the hot path.
func (r *Reconciler) schedulePortCheck(ctx context.Context, resource *v1alpha1.StackResource, key portcheck.Key) bool {
	podIP, found := r.representativePodIP(ctx, resource, key.Revision)
	if !found {
		return false
	}
	r.PortVerifier.Enqueue(key, podIP, declaredPortNumbers(resource))
	return true
}

// representativePodIP returns the IP of one Running pod of the current
// revision. Called only on a cache miss.
func (r *Reconciler) representativePodIP(ctx context.Context, resource *v1alpha1.StackResource, revision string) (string, bool) {
	if r.UncachedClient == nil {
		return "", false
	}
	if resource.Spec.WorkloadType == v1alpha1.WorkloadTypeStatefulService {
		return r.statefulSetPodIP(ctx, resource, revision)
	}
	return r.deploymentPodIP(ctx, resource, revision)
}

// deploymentPodIP resolves the pod IP through the ReplicaSet that owns the
// given deployment revision. The ReplicaSet lookup mirrors
// captureLastFailureDetails so both paths agree on which revision is current.
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

// statefulSetPodIP resolves the pod IP for a StatefulSet, which has no
// ReplicaSet: its pods carry the controller revision directly.
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

// firstDialablePodIP picks a pod that can actually be dialed. A pod that is
// Running with an assigned IP is dialable even though it is not Ready —
// precisely the state a wrong-port workload sits in.
func firstDialablePodIP(pods []corev1.Pod) (string, bool) {
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			return pod.Status.PodIP, true
		}
	}
	return "", false
}

// reportPortNotListening records the proven-dead port on the resource status.
// Callers stop the sub-reconciler chain afterwards: the workload is serving
// traffic nobody answers, which is not Available.
//
// Converged is driven False alongside Available. The replica counts that made
// the workload look converged are still true, but a rollout that lands a
// workload nobody can reach has not delivered the resource the user asked for,
// and reporting "fully converged" next to "not available" is precisely the
// mixed signal the Available/Converged alignment work removed elsewhere.
func (r *Reconciler) reportPortNotListening(resource *v1alpha1.StackResource) {
	msg := resource.Status.LastFailureDetails[0].LastTerminationMessage
	r.Status.SetCondition(resource, v1alpha1.StackResourceWorkloadAvailable, false, "PortNotListening", msg)
	r.Status.SetCondition(resource, v1alpha1.StackResourceConverged, false, "PortNotListening", msg)
	r.Status.ReportNotReady(resource, "PortNotListening", msg)
}
