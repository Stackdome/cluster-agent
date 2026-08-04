package stackresource

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

// deriveSummaryStatus is the ONLY writer of the summary status (Available,
// Stalled, Converged, Phase). Mirrors stack/aggregate.go one level down.
//
//   - Sub-reconcilers contribute domain conditions (WorkloadAvailable,
//     WorkloadConverged, BuildReady, ...) and pass-scoped verdicts. This runs
//     once after the whole chain, so chain order cannot change the outcome.
//   - Available and Converged mirror the workload's firsthand facts. An absent
//     verdict is not by itself evidence of health.
func deriveSummaryStatus(resource *v1alpha1.StackResource, verdicts *controller.VerdictCollector) {
	resource.Status.ObservedGeneration = resource.Generation
	if rev, ok := resource.Annotations[v1alpha1.RevisionAnnotation]; ok {
		resource.Status.ObservedRevision = rev
	}

	switch {
	case verdicts.Failed() != nil:
		deriveFailed(resource, verdicts.Failed())
	case verdicts.NotReady() != nil:
		deriveNotReady(resource, verdicts.NotReady())
	default:
		deriveNoVerdict(resource)
	}

	resource.Status.StatusHash = resource.StatusHash()
}

// deriveFailed handles a terminal verdict. It rides Stalled and Converged, but
// not Available: a workload can serve traffic and still be failed, as when a
// declared secondary port never listened.
func deriveFailed(resource *v1alpha1.StackResource, v *controller.Verdict) {
	resource.Status.Phase = v1alpha1.StackResourcePhaseFailed
	setSummaryCondition(resource, v1alpha1.StackResourceStalled, true, v.Reason, v.Message)
	setSummaryCondition(resource, v1alpha1.StackResourceConverged, false, v.Reason, v.Message)

	if domainConditionTrue(resource, v1alpha1.StackResourceWorkloadAvailable) {
		setSummaryCondition(resource, v1alpha1.StackResourceStatusAvailable, true,
			"ServingButStalled", fmt.Sprintf("workload serving; terminal failure: %s", v.Message))
		return
	}
	setSummaryCondition(resource, v1alpha1.StackResourceStatusAvailable, false, v.Reason, v.Message)
}

// deriveNotReady handles a retriable verdict, so Stalled stays False — carrying
// the current reason so a waiting object never shows stale success text.
func deriveNotReady(resource *v1alpha1.StackResource, v *controller.Verdict) {
	resource.Status.Phase = v1alpha1.StackResourcePhasePending
	setSummaryCondition(resource, v1alpha1.StackResourceStatusAvailable, false, v.Reason, v.Message)
	setSummaryCondition(resource, v1alpha1.StackResourceConverged, false, v.Reason, v.Message)
	setSummaryCondition(resource, v1alpha1.StackResourceStalled, false, v.Reason, v.Message)
}

// deriveNoVerdict handles a chain that ran to the end, so the summary mirrors
// the workload's own conditions.
func deriveNoVerdict(resource *v1alpha1.StackResource) {
	// Nothing gated this pass, so the revision the spec points at is the one
	// actually serving.
	if resource.Spec.BuildSpec != nil {
		resource.Status.ImageSourceRevision = resource.Spec.BuildSpec.SourceRevision.GetSourceRevisionString()
	}

	converged, convergedReason, convergedMsg := domainCondition(resource,
		v1alpha1.StackResourceWorkloadConverged, "WorkloadNotConverged", "workload has not converged")
	setSummaryCondition(resource, v1alpha1.StackResourceConverged, converged, convergedReason, convergedMsg)

	resource.Status.Phase = v1alpha1.StackResourcePhaseDegraded
	if converged {
		resource.Status.Phase = v1alpha1.StackResourcePhaseReady
	}

	// An absent or stale WorkloadAvailable means nothing asserted the workload
	// is serving, so the summary must not assert it either.
	available := domainConditionTrue(resource, v1alpha1.StackResourceWorkloadAvailable)
	reason, msg := "WorkloadNotAvailable", "workload is not available"
	switch {
	case available && converged:
		reason, msg = "StackResourceAvailable", "StackResource is available"
	case available:
		reason, msg = "StackResourceAvailable", "workload serving; current rollout not converged"
	}
	setSummaryCondition(resource, v1alpha1.StackResourceStatusAvailable, available, reason, msg)
	// Nothing terminal happened, so Stalled is False — sharing the text so it
	// never shows stale success.
	setSummaryCondition(resource, v1alpha1.StackResourceStalled, false, reason, msg)
}

// setSummaryCondition writes a summary condition. The
// StackResourceDomainCondition split stops sub-reconcilers naming summary types
// in SetCondition, but it only guards the sanctioned helpers: a raw
// meta.SetStatusCondition could still write one. By convention it must not.
func setSummaryCondition(resource *v1alpha1.StackResource, condType v1alpha1.StackResourceStatusCondition, status bool, reason, msg string) {
	condStatus := metav1.ConditionFalse
	if status {
		condStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(condType),
		Status:             condStatus,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            msg,
	})
}

// domainCondition reads a domain condition and the text it carried. A condition
// from an older generation describes a rollout that no longer exists, so it
// reads as false with the fallback text — neither its status nor its reason may
// leak into the summary.
func domainCondition(resource *v1alpha1.StackResource, condType v1alpha1.StackResourceDomainCondition,
	falseReason, falseMsg string) (bool, string, string) {
	c := meta.FindStatusCondition(resource.Status.Conditions, string(condType))
	if c == nil || c.ObservedGeneration != resource.Generation {
		return false, falseReason, falseMsg
	}
	return c.Status == metav1.ConditionTrue, c.Reason, c.Message
}

// domainConditionTrue reports whether a domain condition is True at the current
// generation. A stale True counts as false: it must not soften a Failed verdict
// or promote Ready.
func domainConditionTrue(resource *v1alpha1.StackResource, condType v1alpha1.StackResourceDomainCondition) bool {
	isTrue, _, _ := domainCondition(resource, condType, "", "")
	return isTrue
}
