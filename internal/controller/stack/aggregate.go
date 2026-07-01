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
	// the hub stamped on it. When the Stack carries a release-id, the child
	// must also carry the same release-id — this prevents a race where the
	// Stack is updated to a new release but the child still belongs to the
	// previous one (cache skew between the Stack and StackResource informers
	// can deliver updates out of order).
	isConverged := func(child *v1alpha1.StackResource) bool {
		convergedCondition := findStatusCondition(
			child.Status.Conditions, child.Generation, string(v1alpha1.StackResourceConverged))
		converged := convergedCondition != nil && convergedCondition.Status == metav1.ConditionTrue
		if !managed {
			return converged
		}
		rev := child.Annotations[v1alpha1.RevisionAnnotation]
		return converged && rev != "" && child.Status.ObservedRevision == rev &&
			(releaseID == "" || child.Annotations[v1alpha1.ReleaseIDAnnotation] == releaseID)
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
	availableCount := 0

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
			case managed && healthy && releaseID != "" && child.Annotations[v1alpha1.ReleaseIDAnnotation] != releaseID:
				summary.Message = "converged on a previous release; waiting for current release"
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

		if availableCond != nil && availableCond.Status == metav1.ConditionTrue {
			availableCount++
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

	resourcesConverged := convergedCount == len(stack.Spec.ResourceNames)
	existingDesired := len(stack.Spec.ResourceNames) - len(missingChildren)
	allDesiredServing := existingDesired > 0 && availableCount == existingDesired && len(missingChildren) == 0

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

	// Compute each condition as an independent boolean. Phase is then derived
	// by priority: Failed > Progressing > Degraded > Ready > Pending.
	lc := status.LastConverged

	// Stalled: at least one desired child has a terminal failure (e.g.
	// unsupported workload type). Always wins for phase (Failed).
	stalled := len(stalledChildren) > 0

	// Progressing: children exist but aren't converged, and we believe a
	// rollout is in progress (not stuck). Guards:
	//   - len(missingChildren) == 0: if CRs haven't been created yet, that's
	//     Pending, not Progressing — we can't progress what doesn't exist.
	//   - !stalled: a terminal failure is Failed, not Progressing.
	//   - The revision check distinguishes "active rollout" from "stuck on the
	//     same version." In managed mode (!managed is false), we check whether
	//     the target revision differs from the last converged one:
	//       lc == nil              → first deploy, nothing has converged yet
	//       lc.Revision != rootRev → new revision applied since last convergence
	//     In standalone mode (!managed is true), we skip the revision check
	//     entirely — there are no revisions to compare, so any non-converged
	//     non-stalled state is treated as progressing.
	progressing := !resourcesConverged && len(missingChildren) == 0 && !stalled &&
		(!managed || lc == nil || lc.Revision != rootRev)

	// Degraded: all children are serving traffic (Available=True) but not all
	// are fully converged — e.g. a pod restarting within maxUnavailable
	// tolerance, or a scale-up in progress on the same revision.
	// Requires lc != nil (has previously converged): a first deploy where pods
	// are partially up is Progressing, not Degraded. Since LastConverged is
	// only written for managed stacks, Degraded is effectively managed-only;
	// standalone pod blips show as Progressing instead.
	degraded := allDesiredServing && !resourcesConverged && !stalled && lc != nil

	// Compute context-aware reason/message for the not-ready path.
	notReadyReason, notReadyMsg := "ResourcesNotReady", "not all resources are converged"
	if len(missingChildren) > 0 {
		notReadyReason = "MissingResources"
		notReadyMsg = fmt.Sprintf("missing resources: %s", strings.Join(missingChildren, ", "))
	} else if stalled {
		notReadyReason = "ResourceStalled"
		notReadyMsg = fmt.Sprintf("stalled resources: %s", strings.Join(stalledChildren, ", "))
	}

	notAvailableReason, notAvailableMsg := notReadyReason, notReadyMsg
	if len(orphanedChildren) > 0 && resourcesConverged {
		notAvailableReason = "OrphanedResources"
		notAvailableMsg = fmt.Sprintf("orphaned resources not in spec.resourceNames: %s", strings.Join(orphanedChildren, ", "))
	}

	// Set each condition once.
	setStackCondition(&status, stack.Generation, v1alpha1.StackConditionStalled, stalled, boolStr(stalled, "ResourceStalled", "NotStalled"), boolStr(stalled, notReadyMsg, "no stalled resources"))
	setStackCondition(&status, stack.Generation, v1alpha1.StackConditionResourcesReady, resourcesConverged, boolStr(resourcesConverged, "AllResourcesReady", notReadyReason), boolStr(resourcesConverged, "all resources converged", notReadyMsg))
	setStackCondition(&status, stack.Generation, v1alpha1.StackConditionAvailable, available, boolStr(available, "AllResourcesReady", notAvailableReason), boolStr(available, "all resources converged and no orphaned resources", notAvailableMsg))
	setStackCondition(&status, stack.Generation, v1alpha1.StackConditionDegraded, degraded, boolStr(degraded, "ServingButNotConverged", "NotDegraded"), boolStr(degraded, "all children serving traffic but not all fully converged", "not degraded"))
	setStackCondition(&status, stack.Generation, v1alpha1.StackConditionProgressing, progressing, boolStr(progressing, "RolloutInProgress", "NotProgressing"), boolStr(progressing, "rollout in progress", "no active rollout"))

	// Phase = highest-priority active state.
	switch {
	case stalled:
		status.Phase = v1alpha1.StackFailed
	case progressing:
		status.Phase = v1alpha1.StackProgressing
	case degraded:
		status.Phase = v1alpha1.StackDegraded
	case available:
		status.Phase = v1alpha1.StackReady
	default:
		status.Phase = v1alpha1.StackPending
	}

	return status
}

func boolStr(b bool, whenTrue, whenFalse string) string {
	if b {
		return whenTrue
	}
	return whenFalse
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
