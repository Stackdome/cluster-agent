package stack

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

// Status aggregation is a three-stage pipeline:
//
//  1. collectChildStats — one pass over the desired children producing
//     per-child summaries and the counts every later decision uses.
//  2. deriveState — pure derivation of the condition booleans and the
//     not-ready/not-available explanations from those counts.
//  3. aggregateStackStatus — glue: builds the base status, stamps
//     LastConverged, writes the conditions, and picks the phase.

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

// childConditions holds the generation-current conditions of one child. A nil
// field means the child's controller has said nothing about that aspect for
// the current spec generation.
type childConditions struct {
	converged *metav1.Condition
	available *metav1.Condition
	stalled   *metav1.Condition
}

func currentChildConditions(child *v1alpha1.StackResource) childConditions {
	return childConditions{
		converged: findStatusCondition(child.Status.Conditions, child.Generation, string(v1alpha1.StackResourceConverged)),
		available: findStatusCondition(child.Status.Conditions, child.Generation, string(v1alpha1.StackResourceStatusAvailable)),
		stalled:   findStatusCondition(child.Status.Conditions, child.Generation, string(v1alpha1.StackResourceStalled)),
	}
}

func isTrue(c *metav1.Condition) bool {
	return c != nil && c.Status == metav1.ConditionTrue
}

// childConvergence is the single source of truth for whether a child counts
// as converged and, when it does not, why (as a human-readable message).
// Deciding and explaining live in one function so the two can never drift.
//
// A child is converged when its controller reports Converged=True for the
// current spec generation and (in managed mode) has observed the revision the
// hub stamped on it. When the Stack carries a release-id, the child must also
// carry the same release-id — this prevents a race where the Stack is updated
// to a new release but the child still belongs to the previous one (cache
// skew between the Stack and StackResource informers can deliver updates out
// of order).
func childConvergence(child *v1alpha1.StackResource, conds childConditions, managed bool, releaseID string) (bool, string) {
	childRevision := child.Annotations[v1alpha1.RevisionAnnotation]

	converged := isTrue(conds.converged)
	if managed {
		converged = converged && childRevision != "" && child.Status.ObservedRevision == childRevision &&
			(releaseID == "" || child.Annotations[v1alpha1.ReleaseIDAnnotation] == releaseID)
	}
	if converged {
		return true, ""
	}

	// Not converged: explain why, most specific cause first.
	healthy := isTrue(conds.available)
	switch {
	case managed && childRevision == "":
		return false, "no revision annotation (required for release-managed stacks)"
	case managed && healthy && child.Status.ObservedRevision != childRevision:
		return false, "ready on a previous revision; latest revision not yet observed"
	case managed && healthy && releaseID != "" && child.Annotations[v1alpha1.ReleaseIDAnnotation] != releaseID:
		return false, "converged on a previous release; waiting for current release"
	case isTrue(conds.stalled):
		return false, fmt.Sprintf("resource is stalled: %s", conds.stalled.Reason)
	case healthy:
		return false, "serving traffic but not fully converged"
	case conds.available == nil:
		return false, "no current status from controller"
	case conds.available.Status == metav1.ConditionFalse:
		return false, fmt.Sprintf("not available: %s", conds.available.Reason)
	default:
		return false, "resource is not yet available"
	}
}

// childStats is everything one pass over the children learns. Summaries,
// counts and the missing/stalled lists cover DESIRED resources only
// (spec.resourceNames), iterated in spec order for a stable StatusHash.
// Orphans are reported separately and must never affect Stack readiness —
// they are slated for deletion.
type childStats struct {
	summaries      []v1alpha1.StackResourceSummary
	missing        []string // desired names with no StackResource object (sorted)
	notServing     []string // desired children present but Available!=True (sorted)
	stalled        []string // desired children reporting a terminal failure (sorted)
	orphans        []string // existing children not in spec.resourceNames (sorted)
	convergedCount int      // desired children that pass childConvergence
	availableCount int      // desired children with Available=True at current generation
	desiredCount   int      // len(spec.resourceNames)
}

// allDesiredServing reports whether every desired child exists and is serving
// traffic (Available=True). Any missing child disqualifies the Stack. The
// desiredCount > 0 guard keeps an empty stack out of the Degraded path (which
// is the only caller); serving() handles the empty case as vacuously true.
func (s childStats) allDesiredServing() bool {
	return len(s.missing) == 0 && s.desiredCount > 0 && s.availableCount == s.desiredCount
}

// serving reports whether every desired child is serving traffic. Unlike
// allDesiredServing it is vacuously true for an empty stack, matching the
// Kubernetes convention that a zero-replica workload is Available.
func (s childStats) serving() bool {
	return len(s.missing) == 0 && len(s.notServing) == 0
}

func collectChildStats(stack *v1alpha1.Stack, children []v1alpha1.StackResource, managed bool, releaseID string) childStats {
	childMap := make(map[string]*v1alpha1.StackResource, len(children))
	for i := range children {
		childMap[children[i].Name] = &children[i]
	}

	stats := childStats{
		summaries:    make([]v1alpha1.StackResourceSummary, 0, len(stack.Spec.ResourceNames)),
		desiredCount: len(stack.Spec.ResourceNames),
	}

	for _, name := range stack.Spec.ResourceNames {
		child, exists := childMap[name]
		if !exists {
			stats.missing = append(stats.missing, name)
			stats.summaries = append(stats.summaries, v1alpha1.StackResourceSummary{
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

		conds := currentChildConditions(child)
		converged, notConvergedMsg := childConvergence(child, conds, managed, releaseID)
		if converged {
			stats.convergedCount++
			if managed {
				summary.ConvergedRevision = child.Annotations[v1alpha1.RevisionAnnotation]
			}
		} else {
			summary.Message = notConvergedMsg
		}

		if isTrue(conds.available) {
			stats.availableCount++
		} else {
			stats.notServing = append(stats.notServing, name)
		}
		if isTrue(conds.stalled) {
			stats.stalled = append(stats.stalled, name)
		}

		stats.summaries = append(stats.summaries, summary)
	}

	// Orphans: children that exist but are no longer desired.
	desiredSet := make(map[string]struct{}, len(stack.Spec.ResourceNames))
	for _, name := range stack.Spec.ResourceNames {
		desiredSet[name] = struct{}{}
	}
	for i := range children {
		if _, desired := desiredSet[children[i].Name]; !desired {
			stats.orphans = append(stats.orphans, children[i].Name)
		}
	}

	slices.Sort(stats.missing)
	slices.Sort(stats.notServing)
	slices.Sort(stats.stalled)
	slices.Sort(stats.orphans)
	return stats
}

// stackState is the outcome of the derivation stage: the condition booleans
// that need real reasoning, plus the reason/message pairs for the failure
// paths. resourcesConverged and converged are trivial and computed by the
// caller, which needs them before deriveState runs (the LastConverged stamp
// depends on them and deriveState reads the stamp).
type stackState struct {
	stalled     bool
	progressing bool
	degraded    bool

	progressingReason  string
	progressingMessage string

	// ResourcesReady=False explanation (convergence of desired children,
	// ignoring orphans).
	notReadyReason  string
	notReadyMessage string

	// Converged=False explanation (ResourcesReady plus the no-orphans gate).
	notConvergedReason  string
	notConvergedMessage string

	// Available=False explanation (serving rollup of desired children).
	notServingReason  string
	notServingMessage string
}

// deriveState computes each condition as an independent boolean from the
// child stats. lc is the (possibly just-stamped) LastConverged record.
func deriveState(stats childStats, resourcesConverged, managed bool, rootRev string, lc *v1alpha1.ConvergenceRecord) stackState {
	var state stackState

	// Stalled: at least one desired child has a terminal failure (e.g.
	// unsupported workload type). Always wins for phase (Failed).
	state.stalled = len(stats.stalled) > 0

	// Progressing: children exist but aren't converged, and we believe a
	// rollout is in progress (not stuck). Guards:
	//   - len(missing) == 0: if CRs haven't been created yet, that's Pending,
	//     not Progressing — we can't progress what doesn't exist.
	//   - !stalled: a terminal failure is Failed, not Progressing.
	//   - The revision check distinguishes "active rollout" from "stuck on the
	//     same version." In managed mode we check whether the target revision
	//     differs from the last converged one:
	//       lc == nil              → first deploy, nothing has converged yet
	//       lc.Revision != rootRev → new revision applied since last convergence
	//     In standalone mode we skip the revision check entirely — there are
	//     no revisions to compare, so any non-converged non-stalled state is
	//     treated as progressing.
	// Orphan cleanup is also progressing: every desired child is converged
	// but children removed from the spec still exist and await deletion by the
	// desired-state owner (the release hub prunes StackResources that leave a
	// release; this controller only reports them). Observed set != desired set
	// means the rollout has not settled — that's an in-flight transition, not
	// Pending, and not a regression from Ready.
	orphanCleanup := resourcesConverged && len(stats.orphans) > 0 && !state.stalled
	state.progressing = orphanCleanup ||
		(!resourcesConverged && len(stats.missing) == 0 && !state.stalled &&
			(!managed || lc == nil || lc.Revision != rootRev))

	state.progressingReason, state.progressingMessage = "RolloutInProgress", "rollout in progress"
	if orphanCleanup {
		state.progressingReason = "OrphanCleanup"
		state.progressingMessage = fmt.Sprintf("waiting for orphaned resources to be deleted: %s", strings.Join(stats.orphans, ", "))
	}

	// Degraded: all children are serving traffic (Available=True) but not all
	// are fully converged — e.g. a pod restarting within maxUnavailable
	// tolerance, or a scale-up in progress on the same revision.
	// Requires lc != nil (has previously converged): a first deploy where pods
	// are partially up is Progressing, not Degraded. Since LastConverged is
	// only written for managed stacks, Degraded is effectively managed-only;
	// standalone pod blips show as Progressing instead.
	state.degraded = stats.allDesiredServing() && !resourcesConverged && !state.stalled && lc != nil

	// Context-aware reason/message for the not-ready path: missing children
	// take precedence over stalled ones, everything else is the generic
	// "not converged" explanation.
	state.notReadyReason, state.notReadyMessage = "ResourcesNotReady", "not all resources are converged"
	if len(stats.missing) > 0 {
		state.notReadyReason = "MissingResources"
		state.notReadyMessage = fmt.Sprintf("missing resources: %s", strings.Join(stats.missing, ", "))
	} else if state.stalled {
		state.notReadyReason = "ResourceStalled"
		state.notReadyMessage = fmt.Sprintf("stalled resources: %s", strings.Join(stats.stalled, ", "))
	}

	// Not-converged inherits the not-ready explanation unless orphans are the
	// only thing left blocking convergence.
	state.notConvergedReason, state.notConvergedMessage = state.notReadyReason, state.notReadyMessage
	if len(stats.orphans) > 0 && resourcesConverged {
		state.notConvergedReason = "OrphanedResources"
		state.notConvergedMessage = fmt.Sprintf("orphaned resources not in spec.resourceNames: %s", strings.Join(stats.orphans, ", "))
	}

	// Not-serving explanation for the Available condition. Missing children
	// take precedence (they cannot serve); otherwise name the present-but-not
	// -serving children.
	state.notServingReason, state.notServingMessage = "ResourcesNotServing", "not all resources are serving traffic"
	if len(stats.missing) > 0 {
		state.notServingReason = "MissingResources"
		state.notServingMessage = fmt.Sprintf("missing resources: %s", strings.Join(stats.missing, ", "))
	} else if len(stats.notServing) > 0 {
		state.notServingMessage = fmt.Sprintf("resources not serving: %s", strings.Join(stats.notServing, ", "))
	}

	return state
}

func aggregateStackStatus(stack *v1alpha1.Stack, children []v1alpha1.StackResource) v1alpha1.StackStatus {
	status := v1alpha1.StackStatus{
		ObservedGeneration: stack.Generation,
		Conditions:         slices.Clone(stack.Status.Conditions),
		LastConverged:      stack.Status.LastConverged,
	}

	// Managed mode: the release hub stamped a revision on the Stack; children
	// must prove they observed the same revision (and release) to count as
	// converged.
	rootRev := stack.Annotations[v1alpha1.RevisionAnnotation]
	managed := rootRev != ""
	if managed {
		status.TargetRevision = rootRev
	}
	releaseID := stack.Annotations[v1alpha1.ReleaseIDAnnotation]

	// Stage 1: one pass over the children — summaries plus counts.
	stats := collectChildStats(stack, children, managed, releaseID)
	status.Resources = stats.summaries
	status.OrphanedResources = stats.orphans

	resourcesConverged := stats.convergedCount == stats.desiredCount
	// converged: every desired child fully rolled out AND no orphans remain.
	// This is the "spec fully settled" verdict — it drives Ready and the
	// LastConverged stamp.
	converged := resourcesConverged && len(stats.orphans) == 0
	// serving: every desired child is Available=True — a pure traffic rollup,
	// independent of convergence. Mirrors the child Available condition.
	serving := stats.serving()

	// Write lastConverged with a write-once-per-target guard. Stamped before
	// deriveState so the progressing check compares against the fresh record.
	if converged && managed {
		if lc := status.LastConverged; lc == nil || lc.Revision != rootRev || lc.ReleaseID != releaseID {
			status.LastConverged = &v1alpha1.ConvergenceRecord{
				Revision:  rootRev,
				ReleaseID: releaseID,
				At:        metav1.Now(),
			}
		}
	}

	// Stage 2: derive the remaining condition booleans and explanations.
	state := deriveState(stats, resourcesConverged, managed, rootRev, status.LastConverged)

	// Stage 3: set each condition once, as an independent boolean.
	if state.stalled {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionStalled, true, "ResourceStalled", state.notReadyMessage)
	} else {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionStalled, false, "NotStalled", "no stalled resources")
	}

	if resourcesConverged {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionResourcesReady, true, "AllResourcesReady", "all resources converged")
	} else {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionResourcesReady, false, state.notReadyReason, state.notReadyMessage)
	}

	// Available = serving: every desired child is Available=True. Stays True
	// during a rollout while old-revision pods keep serving.
	if serving {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionAvailable, true, "AllResourcesServing", "all resources serving traffic")
	} else {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionAvailable, false, state.notServingReason, state.notServingMessage)
	}

	// Converged = settled: all desired children rolled out and no orphans.
	if converged {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionConverged, true, "AllResourcesConverged", "all resources converged and no orphaned resources")
	} else {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionConverged, false, state.notConvergedReason, state.notConvergedMessage)
	}

	if state.degraded {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionDegraded, true, "ServingButNotConverged", "all children serving traffic but not all fully converged")
	} else {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionDegraded, false, "NotDegraded", "not degraded")
	}

	if state.progressing {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionProgressing, true, state.progressingReason, state.progressingMessage)
	} else {
		setStackCondition(&status, stack.Generation, v1alpha1.StackConditionProgressing, false, "NotProgressing", "no active rollout")
	}

	// Phase = highest-priority active state: Failed > Progressing > Degraded >
	// Ready > Pending.
	switch {
	case state.stalled:
		status.Phase = v1alpha1.StackFailed
	case state.progressing:
		status.Phase = v1alpha1.StackProgressing
	case state.degraded:
		status.Phase = v1alpha1.StackDegraded
	case converged:
		status.Phase = v1alpha1.StackReady
	default:
		status.Phase = v1alpha1.StackPending
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
