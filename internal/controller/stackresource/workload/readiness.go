package workload

import (
	"context"
	"time"

	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
	"stackdome.io/cluster-agent/pkg/portcheck"
)

const (
	// DefaultPortCheckGrace is how long a closed-port verdict on a serving
	// workload is re-verified before it is believed.
	DefaultPortCheckGrace = 3 * time.Minute

	portCheckRequeueInterval = 10 * time.Second
)

// ContainerName must match the resource name or the hub's
// MapLastFailureDetails drops the detail.
func readinessFailureDetail(resourceName string, result portcheck.Result) v1alpha1.LastFailureDetail {
	return v1alpha1.LastFailureDetail{
		Type:                   v1alpha1.FailureTypeReadinessFailure,
		ContainerName:          resourceName,
		LastTerminationReason:  "PortNotListening",
		LastTerminationMessage: result.Message(),
	}
}

func declaredPortNumbers(resource *v1alpha1.StackResource) []int32 {
	ports := make([]int32, 0, len(resource.Spec.Ports))
	for _, p := range resource.Spec.Ports {
		ports = append(ports, p.Number)
	}
	return ports
}

// Keyed by revision so a rollout invalidates the previous answer.
func portCheckKey(resource *v1alpha1.StackResource, revision string) portcheck.Key {
	return portcheck.Key{
		Namespace: resource.Namespace,
		Name:      resource.Name,
		Revision:  revision,
	}
}

// A nil verifier disables the check (unit tests, reduced deployments).
func (r *Reconciler) portVerificationApplies(resource *v1alpha1.StackResource) bool {
	return r.PortVerifier != nil && len(resource.Spec.Ports) > 0
}

func (r *Reconciler) portCheckGrace() time.Duration {
	if r.PortCheckGrace <= 0 {
		return DefaultPortCheckGrace
	}
	return r.PortCheckGrace
}

// verifyServingPorts checks the declared ports of a workload whose kubelet
// probe has passed. Never dials inline.
//
// failed: a closed verdict outlived the grace window; failure detail is on
// status, caller should mark the resource unavailable. shouldRequeue: no
// answer yet, caller should requeue to read it.
//
// The grace window (Status.PortCheck.FailingSince) starts at the first closed
// verdict seen while serving, per revision, and survives operator restarts.
// Inside the window closed verdicts are discarded and redialed, so a refusal
// cached during boot never condemns the app and a slow secondary port gets
// time to bind.
func (r *Reconciler) verifyServingPorts(ctx context.Context, resource *v1alpha1.StackResource, revision string) (failed, shouldRequeue bool) {
	if !r.portVerificationApplies(resource) {
		resource.Status.PortCheck = nil
		return false, false
	}
	key := portCheckKey(resource, revision)

	result, done := r.PortVerifier.Get(key)
	if !done {
		// Requeue only if an answer can arrive: dial admitted or in flight.
		if r.schedulePortCheck(ctx, resource, key) || r.PortVerifier.Pending(key) {
			return false, true
		}
		// A dial can land between Get and Enqueue: Enqueue then refuses and
		// Pending is already clear. Re-read so that verdict is processed now
		// instead of sitting cached until the next resync.
		if result, done = r.PortVerifier.Get(key); !done {
			return false, false
		}
	}
	if result.AllOpen() {
		resource.Status.PortCheck = nil
		return false, false
	}

	pc := resource.Status.PortCheck
	if pc == nil || pc.Revision != revision {
		resource.Status.PortCheck = &v1alpha1.PortCheckStatus{Revision: revision, FailingSince: metav1.Now()}
		pc = resource.Status.PortCheck
	}
	// Still in the grace window, requeue the port check.
	if time.Since(pc.FailingSince.Time) < r.portCheckGrace() {
		r.PortVerifier.Forget(key)
		r.schedulePortCheck(ctx, resource, key)
		return false, true
	}
	// Terminal per revision: keep the cached verdict so a dead port doesn't
	// poll forever. A new rollout starts fresh.
	resource.Status.LastFailureDetails = []v1alpha1.LastFailureDetail{
		readinessFailureDetail(resource.Name, result),
	}
	return true, false
}

// capturePortDiagnosis records a PortNotListening detail for a workload that
// is not serving. Provisional: verifyServingPorts re-verifies once serving.
func (r *Reconciler) capturePortDiagnosis(ctx context.Context, resource *v1alpha1.StackResource, revision string) {
	if !r.portVerificationApplies(resource) {
		return
	}
	key := portCheckKey(resource, revision)
	result, done := r.PortVerifier.Get(key)
	if !done {
		r.schedulePortCheck(ctx, resource, key)
		return
	}
	if result.AllOpen() {
		return
	}
	resource.Status.LastFailureDetails = []v1alpha1.LastFailureDetail{
		readinessFailureDetail(resource.Name, result),
	}
}

// schedulePortCheck queues a check and reports whether a dial was admitted.
// On refusal (no dialable pod, in flight, queue full) the caller's requeue is
// the retry.
func (r *Reconciler) schedulePortCheck(ctx context.Context, resource *v1alpha1.StackResource, key portcheck.Key) bool {
	podIP, found := r.representativePodIP(ctx, resource, key.Revision)
	if !found {
		return false
	}
	return r.PortVerifier.Enqueue(key, podIP, declaredPortNumbers(resource))
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

// Converged goes False with Available: "fully converged" next to "not
// available" is a mixed signal. The chain keeps running; conditions carry the
// verdict.
func (r *Reconciler) reportPortNotListening(ctx context.Context, resource *v1alpha1.StackResource) {
	msg := resource.Status.LastFailureDetails[0].LastTerminationMessage
	r.Status.SetCondition(resource, v1alpha1.StackResourceWorkloadAvailable, false, "PortNotListening", msg)
	r.Status.SetCondition(resource, v1alpha1.StackResourceWorkloadConverged, false, "PortNotListening", msg)
	r.Status.ReportNotReady(ctx, resource, "PortNotListening", msg)
}
