package stackresource

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

// deriveSummaryStatus is the ONLY writer of the summary status
// (Available, Stalled, Converged, Phase). Sub-reconcilers contribute domain
// conditions (WorkloadAvailable, WorkloadConverged, BuildReady, ...) and
// pass-scoped verdicts; this derives the summary once per pass, after the
// whole chain has run, so chain order can never change the outcome. The full
// contract is the Derivation Matrix in
// docs/superpowers/plans/2026-08-01-single-writer-status-derivation.md.
// Mirrors stack/aggregate.go one level down.
func deriveSummaryStatus(resource *v1alpha1.StackResource, verdicts *controller.VerdictCollector) {
	resource.Status.ObservedGeneration = resource.Generation
	if rev, ok := resource.Annotations[v1alpha1.RevisionAnnotation]; ok {
		resource.Status.ObservedRevision = rev
	}
	if resource.Spec.BuildSpec != nil {
		resource.Status.ImageSourceRevision = resource.Spec.BuildSpec.SourceRevision.GetSourceRevisionString()
	}

	switch {
	case verdicts.Failed() != nil:
		v := verdicts.Failed()
		resource.Status.Phase = v1alpha1.StackResourcePhaseFailed
		setSummaryCondition(resource, v1alpha1.StackResourceStalled, true, v.Reason, v.Message)
		setSummaryCondition(resource, v1alpha1.StackResourceConverged, false, v.Reason, v.Message)
		if domainConditionTrue(resource, v1alpha1.StackResourceWorkloadAvailable) {
			// Terminal failure on a workload that is still serving (e.g. a
			// declared secondary port never listened): Available reflects the
			// serving truth, the verdict rides Stalled/Converged.
			setSummaryCondition(resource, v1alpha1.StackResourceStatusAvailable, true,
				"ServingButStalled", fmt.Sprintf("workload serving; terminal failure: %s", v.Message))
		} else {
			setSummaryCondition(resource, v1alpha1.StackResourceStatusAvailable, false, v.Reason, v.Message)
		}

	case verdicts.NotReady() != nil:
		v := verdicts.NotReady()
		resource.Status.Phase = v1alpha1.StackResourcePhasePending
		setSummaryCondition(resource, v1alpha1.StackResourceStatusAvailable, false, v.Reason, v.Message)
		setSummaryCondition(resource, v1alpha1.StackResourceConverged, false, v.Reason, v.Message)
		// Retriable, so Stalled stays False — with the current reason so a
		// waiting object never carries stale success text.
		setSummaryCondition(resource, v1alpha1.StackResourceStalled, false, v.Reason, v.Message)

	default:
		// Summary Converged mirrors the workload's firsthand fact, carrying
		// its reason/message through.
		wc := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceWorkloadConverged))
		converged := domainConditionTrue(resource, v1alpha1.StackResourceWorkloadConverged)
		convergedReason, convergedMsg := "WorkloadNotConverged", "workload has not converged"
		if wc != nil {
			convergedReason, convergedMsg = wc.Reason, wc.Message
		}
		setSummaryCondition(resource, v1alpha1.StackResourceConverged, converged, convergedReason, convergedMsg)

		availableMsg := "StackResource is available"
		if converged {
			resource.Status.Phase = v1alpha1.StackResourcePhaseReady
		} else {
			resource.Status.Phase = v1alpha1.StackResourcePhaseDegraded
			availableMsg = "workload serving; current rollout not converged"
		}
		setSummaryCondition(resource, v1alpha1.StackResourceStatusAvailable, true, "StackResourceAvailable", availableMsg)
		setSummaryCondition(resource, v1alpha1.StackResourceStalled, false, "StackResourceAvailable", "StackResource is available")
	}

	resource.Status.StatusHash = resource.StatusHash()
}

// setSummaryCondition writes a summary condition. Unexported and called only
// by deriveSummaryStatus — sub-reconcilers cannot even name summary condition
// types in SetCondition thanks to the StackResourceDomainCondition split.
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

// domainConditionTrue reports whether a domain condition is True at the
// current generation. A stale True from a previous generation counts as
// false — it must not soften a Failed verdict or promote Ready.
func domainConditionTrue(resource *v1alpha1.StackResource, condType v1alpha1.StackResourceDomainCondition) bool {
	c := meta.FindStatusCondition(resource.Status.Conditions, string(condType))
	return c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration == resource.Generation
}
