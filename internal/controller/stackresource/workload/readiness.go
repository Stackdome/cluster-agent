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

// withinPortCheckGrace reports whether a workload that converged recently
// enough is still worth requeueing for an outstanding verification. It is the
// StatefulSet counterpart to the Deployment's rollout-settled window:
// StatefulSets expose no rollout deadline, so convergence is the only anchor
// available. A resource with no convergence record is not polled at all, since
// there is nothing to bound the polling with.
func (r *Reconciler) withinPortCheckGrace(resource *v1alpha1.StackResource) bool {
	converged := resource.Status.LastConverged
	if converged == nil {
		return false
	}
	return time.Since(converged.At.Time) < r.portCheckGrace()
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
func (r *Reconciler) capturePortVerification(ctx context.Context, resource *v1alpha1.StackResource, deploymentRevision string) bool {
	if !r.portVerificationApplies(resource) {
		return false
	}
	// Keyed by deployment revision rather than pod-template-hash: the revision
	// is already in hand, so a cache hit needs no pod lookup at all, and a new
	// rollout still invalidates the previous answer.
	key := portCheckKey(resource, deploymentRevision)

	result, done := r.PortVerifier.Get(key)
	if !done {
		// Only a cache miss pays for the pod lookup.
		podIP, found := r.representativePodIP(ctx, resource, deploymentRevision)
		if !found {
			return false
		}
		r.PortVerifier.Enqueue(key, podIP, declaredPortNumbers(resource))
		return false
	}
	if result.AllOpen() {
		return false
	}
	resource.Status.LastFailureDetails = []v1alpha1.LastFailureDetail{
		readinessFailureDetail(resource.Name, result),
	}
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
func (r *Reconciler) reportPortNotListening(resource *v1alpha1.StackResource) {
	msg := resource.Status.LastFailureDetails[0].LastTerminationMessage
	r.Status.SetCondition(resource, v1alpha1.StackResourceWorkloadAvailable, false, "PortNotListening", msg)
	r.Status.ReportNotReady(resource, "PortNotListening", msg)
}
