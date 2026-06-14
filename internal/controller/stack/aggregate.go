package stack

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

// findStatusCondition returns the condition only if its ObservedGeneration matches
// the object's current generation. A stale or absent condition is treated as
// unknown — never as a verdict.
func findStatusCondition(conds []metav1.Condition, generation int64, condType string) *metav1.Condition {
	cond := meta.FindStatusCondition(conds, condType)
	if cond == nil || cond.ObservedGeneration != generation {
		return nil
	}
	return cond
}

func aggregateStackStatus(stack *v1alpha1.Stack, children []v1alpha1.StackResource) v1alpha1.StackStatus {
	status := v1alpha1.StackStatus{
		ObservedGeneration: stack.Generation,
		Conditions:         slices.Clone(stack.Status.Conditions),
		LastConverged:      stack.Status.LastConverged,
	}

	rootRev := stack.Annotations[v1alpha1.RevisionAnnotation]
	managed := rootRev != ""
	if managed {
		status.TargetRevision = rootRev
	}
	releaseID := stack.Annotations[v1alpha1.ReleaseIDAnnotation]

	childMap := make(map[string]*v1alpha1.StackResource, len(children))
	for i := range children {
		childMap[children[i].Name] = &children[i]
	}
	desiredSet := make(map[string]struct{}, len(stack.Spec.ResourceNames))
	for _, name := range stack.Spec.ResourceNames {
		desiredSet[name] = struct{}{}
	}

	// A child is converged when its controller reports it healthy for the
	// current spec generation and (in managed mode) has observed the revision
	// the hub stamped on it. Single source of truth used by both the summary
	// pass and the readiness counts below.
	isConverged := func(child *v1alpha1.StackResource) bool {
		convergedCondition := findStatusCondition(child.Status.Conditions, child.Generation, string(v1alpha1.StackResourceConverged))
		converged := convergedCondition != nil && convergedCondition.Status == metav1.ConditionTrue
		if !managed {
			return converged
		}
		rev := child.Annotations[v1alpha1.RevisionAnnotation]
		return converged && rev != "" && child.Status.ObservedRevision == rev
	}

	isStalled := func(child *v1alpha1.StackResource) bool {
		c := findStatusCondition(child.Status.Conditions, child.Generation, string(v1alpha1.StackResourceStalled))
		return c != nil && c.Status == metav1.ConditionTrue
	}

	// Summaries, convergence and stalled counts are computed over DESIRED
	// resources only (spec.resourceNames), iterated in spec order for a stable
	// StatusHash. Orphans are reported separately and must never affect Stack
	// readiness — they are slated for deletion.
	summaries := make([]v1alpha1.StackResourceSummary, 0, len(stack.Spec.ResourceNames))
	var missingChildren []string
	var stalledChildren []string
	convergedCount := 0

	for _, name := range stack.Spec.ResourceNames {
		child, exists := childMap[name]
		if !exists {
			missingChildren = append(missingChildren, name)
			summaries = append(summaries, v1alpha1.StackResourceSummary{
				Name:    name,
				Missing: true,
				Message: "resource does not exist",
			})
			continue
		}

		summary := v1alpha1.StackResourceSummary{
			Name:              name,
			Phase:             child.Status.Phase,
			ObservedRevision:  child.Status.ObservedRevision,
			LastConverged:     child.Status.LastConverged,
			Replicas:          child.Status.Replicas,
			AvailableReplicas: child.Status.AvailableReplicas,
			UpdatedReplicas:   child.Status.UpdatedReplicas,
		}

		availableCond := findStatusCondition(child.Status.Conditions, child.Generation, string(v1alpha1.StackResourceStatusAvailable))
		stalledCond := findStatusCondition(child.Status.Conditions, child.Generation, string(v1alpha1.StackResourceStalled))
		childRevision := child.Annotations[v1alpha1.RevisionAnnotation]

		if isConverged(child) {
			convergedCount++
			if managed {
				summary.ConvergedRevision = childRevision
			}
		} else {
			healthy := availableCond != nil && availableCond.Status == metav1.ConditionTrue
			switch {
			case managed && childRevision == "":
				summary.Message = "no revision annotation (required for release-managed stacks)"
			case managed && healthy && child.Status.ObservedRevision != childRevision:
				summary.Message = "ready on a previous revision; latest revision not yet observed"
			case stalledCond != nil && stalledCond.Status == metav1.ConditionTrue:
				summary.Message = fmt.Sprintf("resource is stalled: %s", stalledCond.Reason)
			case healthy:
				summary.Message = "serving traffic but not fully converged"
			case availableCond == nil:
				summary.Message = "no current status from controller"
			case availableCond.Status == metav1.ConditionFalse:
				summary.Message = fmt.Sprintf("not available: %s", availableCond.Reason)
			default:
				summary.Message = "resource is not yet available"
			}
		}

		if isStalled(child) {
			stalledChildren = append(stalledChildren, name)
		}

		summaries = append(summaries, summary)
	}

	var orphanedChildren []string
	for i := range children {
		if _, desired := desiredSet[children[i].Name]; !desired {
			orphanedChildren = append(orphanedChildren, children[i].Name)
		}
	}
	slices.Sort(orphanedChildren)
	slices.Sort(stalledChildren)
	slices.Sort(missingChildren)

	status.Resources = summaries
	status.OrphanedResources = orphanedChildren

	// Every desired resource must be converged. Missing children are never
	// counted (they don't exist) and stalled children are never converged
	// (Available=False), so both naturally fail this check without needing
	// separate guards here — missingChildren/stalledChildren drive the reason
	// and message in the switch below, not readiness itself.
	resourcesConverged := convergedCount == len(stack.Spec.ResourceNames)
	available := resourcesConverged && len(orphanedChildren) == 0

	// Write lastConverged with a write-once-per-target guard.
	if available && managed {
		lc := status.LastConverged
		if lc == nil || lc.Revision != rootRev || lc.ReleaseID != releaseID {
			status.LastConverged = &v1alpha1.ConvergenceRecord{
				Revision:  rootRev,
				ReleaseID: releaseID,
				At:        metav1.Now(),
			}
		}
	}

	// Derive conditions and the display phase. Exactly one case fires; each sets
	// all three remaining conditions so they can never drift out of sync. Order
	// encodes severity: terminal failure first, then full health, then the
	// specific not-ready reasons.
	switch {
	case len(stalledChildren) > 0:
		msg := fmt.Sprintf("stalled resources: %s", strings.Join(stalledChildren, ", "))
		status.Phase = v1alpha1.StackFailed
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionStalled, true, "ResourceStalled", msg)
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionResourcesReady, false, "ResourceStalled", msg)
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionAvailable, false, "ResourceStalled", msg)
	case available:
		status.Phase = v1alpha1.StackReady
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionStalled, false, "NotStalled", "no stalled resources")
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionResourcesReady, true, "AllResourcesReady", "all resources converged")
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionAvailable, true, "AllResourcesReady", "all resources converged and no orphaned resources")
	case len(missingChildren) > 0:
		msg := fmt.Sprintf("missing resources: %s", strings.Join(missingChildren, ", "))
		status.Phase = v1alpha1.StackPending
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionStalled, false, "NotStalled", "no stalled resources")
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionResourcesReady, false, "MissingResources", msg)
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionAvailable, false, "MissingResources", msg)
	case len(orphanedChildren) > 0 && resourcesConverged:
		msg := fmt.Sprintf("orphaned resources not in spec.resourceNames: %s", strings.Join(orphanedChildren, ", "))
		status.Phase = v1alpha1.StackPending
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionStalled, false, "NotStalled", "no stalled resources")
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionResourcesReady, true, "AllResourcesReady", "all resources converged")
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionAvailable, false, "OrphanedResources", msg)
	default:
		status.Phase = v1alpha1.StackPending
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionStalled, false, "NotStalled", "no stalled resources")
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionResourcesReady, false, "ResourcesNotReady", "not all resources are converged")
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionAvailable, false, "ResourcesNotReady", "not all resources are converged")
	}
	return status
}

func setStackCondition(status *v1alpha1.StackStatus, generation int64, condType v1alpha1.StackCondition, ready bool, reason, msg string) {
	condStatus := metav1.ConditionFalse
	if ready {
		condStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               string(condType),
		Status:             condStatus,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            msg,
	})
}
