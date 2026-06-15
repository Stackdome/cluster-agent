package stack

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

const testRev = "rev-1"

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func managedStack(names ...string) *v1alpha1.Stack {
	return &v1alpha1.Stack{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "app",
			Generation: 3,
			Annotations: map[string]string{
				v1alpha1.RevisionAnnotation: testRev,
			},
		},
		Spec: v1alpha1.StackSpec{ResourceNames: names},
	}
}

func standaloneStack(names ...string) *v1alpha1.Stack {
	return &v1alpha1.Stack{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "app",
			Generation: 3,
		},
		Spec: v1alpha1.StackSpec{ResourceNames: names},
	}
}

func readyChild(name, annotatedRev, observedRev string, generation int64) v1alpha1.StackResource {
	sr := v1alpha1.StackResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Generation: generation,
		},
		Status: v1alpha1.StackResourceStatus{
			Phase:            v1alpha1.StackResourcePhaseReady,
			ObservedRevision: observedRev,
			Conditions: []metav1.Condition{
				{
					Type:               string(v1alpha1.StackResourceStatusAvailable),
					Status:             metav1.ConditionTrue,
					ObservedGeneration: generation,
					Reason:             "StackResourceAvailable",
				},
				{
					Type:               string(v1alpha1.StackResourceConverged),
					Status:             metav1.ConditionTrue,
					ObservedGeneration: generation,
					Reason:             "FullyConverged",
				},
			},
		},
	}
	if annotatedRev != "" {
		sr.Annotations = map[string]string{v1alpha1.RevisionAnnotation: annotatedRev}
	}
	return sr
}

func servingNotConvergedChild(name, annotatedRev, observedRev string, generation int64) v1alpha1.StackResource {
	sr := readyChild(name, annotatedRev, observedRev, generation)
	meta.SetStatusCondition(&sr.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceConverged),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: generation,
		Reason:             "NotConverged",
	})
	return sr
}

func stalledChild(name, annotatedRev string, generation int64) v1alpha1.StackResource {
	sr := v1alpha1.StackResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Generation: generation,
		},
		Status: v1alpha1.StackResourceStatus{
			Phase:            v1alpha1.StackResourcePhaseFailed,
			ObservedRevision: annotatedRev,
			Conditions: []metav1.Condition{
				{
					Type:               string(v1alpha1.StackResourceStatusAvailable),
					Status:             metav1.ConditionFalse,
					ObservedGeneration: generation,
					Reason:             "WorkloadTypeNotSupported",
				},
				{
					Type:               string(v1alpha1.StackResourceStalled),
					Status:             metav1.ConditionTrue,
					ObservedGeneration: generation,
					Reason:             "WorkloadTypeNotSupported",
				},
			},
		},
	}
	if annotatedRev != "" {
		sr.Annotations = map[string]string{v1alpha1.RevisionAnnotation: annotatedRev}
	}
	return sr
}

func pendingChild(name, annotatedRev string, generation int64) v1alpha1.StackResource {
	sr := v1alpha1.StackResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Generation: generation,
		},
		Status: v1alpha1.StackResourceStatus{
			Phase: v1alpha1.StackResourcePhasePending,
			Conditions: []metav1.Condition{
				{
					Type:               string(v1alpha1.StackResourceStatusAvailable),
					Status:             metav1.ConditionFalse,
					ObservedGeneration: generation,
					Reason:             "Reconciling",
				},
			},
		},
	}
	if annotatedRev != "" {
		sr.Annotations = map[string]string{v1alpha1.RevisionAnnotation: annotatedRev}
	}
	return sr
}

func withReleaseID(sr v1alpha1.StackResource, id string) v1alpha1.StackResource {
	if sr.Annotations == nil {
		sr.Annotations = make(map[string]string)
	}
	sr.Annotations[v1alpha1.ReleaseIDAnnotation] = id
	return sr
}

func findCond(t *testing.T, conds []metav1.Condition, condType string) metav1.Condition {
	t.Helper()
	for _, c := range conds {
		if c.Type == condType {
			return c
		}
	}
	t.Fatalf("condition %q not found", condType)
	return metav1.Condition{}
}

func assertPhase(t *testing.T, status v1alpha1.StackStatus, want v1alpha1.StackPhase) {
	t.Helper()
	if status.Phase != want {
		t.Fatalf("expected phase %s, got %s", want, status.Phase)
	}
}

func assertCondition(t *testing.T, status v1alpha1.StackStatus, condType string, wantStatus metav1.ConditionStatus) {
	t.Helper()
	cond := findCond(t, status.Conditions, condType)
	if cond.Status != wantStatus {
		t.Fatalf("expected %s=%s, got %s (reason: %s)", condType, wantStatus, cond.Status, cond.Reason)
	}
}

// ---------------------------------------------------------------------------
// Phase: Ready
// ---------------------------------------------------------------------------

func TestManagedAllConverged(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		readyChild("worker", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackReady)
	if status.LastConverged == nil || status.LastConverged.Revision != testRev {
		t.Fatalf("expected lastConverged.revision=%q, got %+v", testRev, status.LastConverged)
	}
	assertCondition(t, status, string(v1alpha1.StackConditionAvailable), metav1.ConditionTrue)
	assertCondition(t, status, string(v1alpha1.StackConditionResourcesReady), metav1.ConditionTrue)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionFalse)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionFalse)
}

func TestStandaloneConvergesOnReadyAlone(t *testing.T) {
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{
		readyChild("web", "", "", 1),
	})
	assertPhase(t, status, v1alpha1.StackReady)
	if status.LastConverged != nil {
		t.Fatal("lastConverged must not be set in standalone mode")
	}
	assertCondition(t, status, string(v1alpha1.StackConditionAvailable), metav1.ConditionTrue)
	assertCondition(t, status, string(v1alpha1.StackConditionResourcesReady), metav1.ConditionTrue)
}

func TestEmptyResourceNames(t *testing.T) {
	status := aggregateStackStatus(managedStack(), nil)
	assertPhase(t, status, v1alpha1.StackReady)
}

// ---------------------------------------------------------------------------
// Phase: Failed (stalled)
// ---------------------------------------------------------------------------

func TestStalledChildPropagates(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackFailed)
	assertCondition(t, status, string(v1alpha1.StackConditionStalled), metav1.ConditionTrue)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionFalse)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionFalse)
	assertCondition(t, status, string(v1alpha1.StackConditionAvailable), metav1.ConditionFalse)
	assertCondition(t, status, string(v1alpha1.StackConditionResourcesReady), metav1.ConditionFalse)
}

func TestStalledBeatsMissing(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackFailed)
}

func TestStalledBeatsProgressing(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "old-rev"}
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackFailed)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionFalse)
}

func TestMixedConvergedAndStalled(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		stalledChild("worker", testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackFailed)
	assertCondition(t, status, string(v1alpha1.StackConditionResourcesReady), metav1.ConditionFalse)
}

func TestStaleStalledIgnored(t *testing.T) {
	child := stalledChild("web", testRev, 1)
	child.Generation = 2
	child.Status.Conditions = append(child.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceStatusAvailable),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: 2,
		Reason:             "Reconciling",
	})
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{child})
	if status.Phase == v1alpha1.StackFailed {
		t.Fatal("stale Stalled condition should not cause Failed")
	}
	assertPhase(t, status, v1alpha1.StackProgressing)
	assertCondition(t, status, string(v1alpha1.StackConditionStalled), metav1.ConditionFalse)
}

func TestStandaloneStalledChild(t *testing.T) {
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{
		stalledChild("web", "", 1),
	})
	assertPhase(t, status, v1alpha1.StackFailed)
}

func TestStalledClearsOnRecovery(t *testing.T) {
	stack := managedStack("web")
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackConditionStalled),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: 2,
		Reason:             "ResourceStalled",
	})
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackReady)
	assertCondition(t, status, string(v1alpha1.StackConditionStalled), metav1.ConditionFalse)
}

// ---------------------------------------------------------------------------
// Condition carryover: old conditions on stack.Status must be overwritten
// ---------------------------------------------------------------------------

func TestDegradedClearedOnRecovery(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionDegraded),
		Status: metav1.ConditionTrue,
		Reason: "ServingButNotConverged",
	})
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackReady)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionFalse)
}

func TestProgressingClearedOnConvergence(t *testing.T) {
	stack := managedStack("web")
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionProgressing),
		Status: metav1.ConditionTrue,
		Reason: "RolloutInProgress",
	})
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackReady)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionFalse)
}

func TestProgressingToDegraded(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionProgressing),
		Status: metav1.ConditionTrue,
		Reason: "RolloutInProgress",
	})
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		servingNotConvergedChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackDegraded)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionFalse)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionTrue)
}

func TestDegradedToProgressing(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.RevisionAnnotation] = "rev-2"
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionDegraded),
		Status: metav1.ConditionTrue,
		Reason: "ServingButNotConverged",
	})
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		servingNotConvergedChild("web", "rev-2", "rev-2", 1),
	})
	assertPhase(t, status, v1alpha1.StackProgressing)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionTrue)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionTrue)
}

func TestAvailableClearedWhenChildRegresses(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionAvailable),
		Status: metav1.ConditionTrue,
		Reason: "AllResourcesReady",
	})
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionResourcesReady),
		Status: metav1.ConditionTrue,
		Reason: "AllResourcesReady",
	})
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})
	assertCondition(t, status, string(v1alpha1.StackConditionAvailable), metav1.ConditionFalse)
	assertCondition(t, status, string(v1alpha1.StackConditionResourcesReady), metav1.ConditionFalse)
}

func TestResourcesReadyRestoredOnConvergence(t *testing.T) {
	stack := managedStack("web")
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionResourcesReady),
		Status: metav1.ConditionFalse,
		Reason: "ResourcesNotReady",
	})
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionAvailable),
		Status: metav1.ConditionFalse,
		Reason: "ResourcesNotReady",
	})
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackReady)
	assertCondition(t, status, string(v1alpha1.StackConditionResourcesReady), metav1.ConditionTrue)
	assertCondition(t, status, string(v1alpha1.StackConditionAvailable), metav1.ConditionTrue)
}

func TestStalledClearedWhenChildRecovers(t *testing.T) {
	stack := managedStack("web")
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionStalled),
		Status: metav1.ConditionTrue,
		Reason: "ResourceStalled",
	})
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionAvailable),
		Status: metav1.ConditionFalse,
		Reason: "ResourceStalled",
	})
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})
	assertCondition(t, status, string(v1alpha1.StackConditionStalled), metav1.ConditionFalse)
	assertPhase(t, status, v1alpha1.StackProgressing)
}

func TestFailedClearsDegradedAndProgressing(t *testing.T) {
	stack := managedStack("web")
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionDegraded),
		Status: metav1.ConditionTrue,
		Reason: "ServingButNotConverged",
	})
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:   string(v1alpha1.StackConditionProgressing),
		Status: metav1.ConditionTrue,
		Reason: "RolloutInProgress",
	})
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackFailed)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionFalse)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionFalse)
}

// ---------------------------------------------------------------------------
// Phase: Progressing
// ---------------------------------------------------------------------------

func TestProgressingFirstDeployManaged(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackProgressing)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionTrue)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionFalse)
}

func TestProgressingFirstDeployStandalone(t *testing.T) {
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{
		pendingChild("web", "", 1),
	})
	assertPhase(t, status, v1alpha1.StackProgressing)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionTrue)
}

func TestProgressingMidDeploy(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.RevisionAnnotation] = "rev-2"
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "rev-1", ReleaseID: "uuid-1", At: metav1.Now()}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", "rev-2", "rev-1", 1),
	})
	assertPhase(t, status, v1alpha1.StackProgressing)
	if status.LastConverged.Revision != "rev-1" {
		t.Fatalf("lastConverged must stay rev-1 until the child converges, got %+v", status.LastConverged)
	}
}

func TestProgressingRollingUpdateWithOldPodsServing(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.RevisionAnnotation] = "rev-2"
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "rev-1"}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		servingNotConvergedChild("web", "rev-2", "rev-2", 1),
	})
	assertPhase(t, status, v1alpha1.StackProgressing)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionTrue)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionTrue)
}

func TestProgressingBeatsDegragedPhase(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.RevisionAnnotation] = "rev-2"
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "rev-1"}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		servingNotConvergedChild("web", "rev-2", "rev-2", 1),
	})
	assertPhase(t, status, v1alpha1.StackProgressing)
}

func TestProgressingServingButNotConvergedFirstDeploy(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		servingNotConvergedChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackProgressing)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionFalse)
	if status.LastConverged != nil {
		t.Fatal("lastConverged must not be written on first deploy")
	}
}

func TestProgressingStaleRevision(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, "rev-0", 1),
	})
	assertPhase(t, status, v1alpha1.StackProgressing)
}

// ---------------------------------------------------------------------------
// Phase: Degraded
// ---------------------------------------------------------------------------

func TestDegradedPodBlipSameRevision(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		servingNotConvergedChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackDegraded)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionTrue)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionFalse)
}

func TestDegradedClearsOnConvergence(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackReady)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionFalse)
}

func TestDegradedNotOnFirstDeploy(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		servingNotConvergedChild("web", testRev, testRev, 1),
	})
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionFalse)
}

func TestDegradedMixedConvergedAndServing(t *testing.T) {
	stack := managedStack("web", "worker")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		servingNotConvergedChild("worker", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackDegraded)
	if status.LastConverged.Revision != testRev {
		t.Fatalf("lastConverged should stay at %s, got %q", testRev, status.LastConverged.Revision)
	}
}

func TestNotDegradedWhenChildNotServing(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionFalse)
	assertPhase(t, status, v1alpha1.StackPending)
}

// ---------------------------------------------------------------------------
// Phase: Pending
// ---------------------------------------------------------------------------

func TestMissingChildIsPending(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackPending)
	if !status.Resources[1].Missing {
		t.Fatal("expected worker to be Missing")
	}
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if ready.Reason != "MissingResources" {
		t.Fatalf("expected reason MissingResources, got %q", ready.Reason)
	}
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionFalse)
}

func TestMissingChildNotProgressing(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackPending)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionFalse)
}

func TestOrphanBlocksAvailableNotResourcesReady(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		readyChild("old-worker", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackPending)
	assertCondition(t, status, string(v1alpha1.StackConditionResourcesReady), metav1.ConditionTrue)
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if avail.Reason != "OrphanedResources" {
		t.Fatalf("expected OrphanedResources, got %q", avail.Reason)
	}
}

func TestFullyDownSameRevisionIsPending(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackPending)
	assertCondition(t, status, string(v1alpha1.StackConditionDegraded), metav1.ConditionFalse)
	assertCondition(t, status, string(v1alpha1.StackConditionProgressing), metav1.ConditionFalse)
}

// ---------------------------------------------------------------------------
// Orphan and missing edge cases
// ---------------------------------------------------------------------------

func TestStalledOrphanDoesNotFailStack(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		stalledChild("old-bad", testRev, 1),
	})
	if status.Phase == v1alpha1.StackFailed {
		t.Fatalf("stalled orphan must not fail the stack; got %s", status.Phase)
	}
	if !slices.Contains(status.OrphanedResources, "old-bad") {
		t.Fatalf("expected old-bad orphan, got %v", status.OrphanedResources)
	}
	assertCondition(t, status, string(v1alpha1.StackConditionStalled), metav1.ConditionFalse)
}

func TestMissingAndOrphanReportsMissing(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		readyChild("old-db", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackPending)
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if ready.Status != metav1.ConditionFalse || ready.Reason != "MissingResources" {
		t.Fatalf("expected ResourcesReady False/MissingResources, got %s/%s", ready.Status, ready.Reason)
	}
}

func TestUnhealthyOrphanDoesNotBlockResourcesReady(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		pendingChild("old-worker", testRev, 1),
	})
	assertCondition(t, status, string(v1alpha1.StackConditionResourcesReady), metav1.ConditionTrue)
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if avail.Reason != "OrphanedResources" {
		t.Fatalf("expected OrphanedResources, got %q", avail.Reason)
	}
}

func TestMultipleOrphansSorted(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		readyChild("zeta", testRev, testRev, 1),
		readyChild("alpha", testRev, testRev, 1),
	})
	if !slices.Equal(status.OrphanedResources, []string{"alpha", "zeta"}) {
		t.Fatalf("orphans should be sorted, got %v", status.OrphanedResources)
	}
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if avail.Reason != "OrphanedResources" {
		t.Fatalf("expected OrphanedResources reason, got %q", avail.Reason)
	}
}

func TestEmptyDesiredWithOrphan(t *testing.T) {
	status := aggregateStackStatus(managedStack(), []v1alpha1.StackResource{
		readyChild("leftover", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackPending)
	assertCondition(t, status, string(v1alpha1.StackConditionResourcesReady), metav1.ConditionTrue)
}

// ---------------------------------------------------------------------------
// lastConverged lifecycle
// ---------------------------------------------------------------------------

func TestLastConvergedSticky(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "old-rev", At: metav1.Now()}
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})
	if status.LastConverged == nil || status.LastConverged.Revision != "old-rev" {
		t.Fatalf("lastConverged must be sticky; expected old-rev, got %+v", status.LastConverged)
	}
}

func TestWriteOnceGuard(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.ReleaseIDAnnotation] = "uuid-1"
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		withReleaseID(readyChild("web", testRev, testRev, 1), "uuid-1"),
	})
	firstAt := status.LastConverged.At
	stack.Status.LastConverged = status.LastConverged
	status2 := aggregateStackStatus(stack, []v1alpha1.StackResource{
		withReleaseID(readyChild("web", testRev, testRev, 1), "uuid-1"),
	})
	if !status2.LastConverged.At.Equal(&firstAt) {
		t.Fatal("write-once guard failed: At changed on same target")
	}
}

func TestRollbackProducesNewRecord(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.ReleaseIDAnnotation] = "uuid-1"
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		withReleaseID(readyChild("web", testRev, testRev, 1), "uuid-1"),
	})
	stack.Status.LastConverged = status.LastConverged
	stack.Annotations[v1alpha1.ReleaseIDAnnotation] = "uuid-2"
	status2 := aggregateStackStatus(stack, []v1alpha1.StackResource{
		withReleaseID(readyChild("web", testRev, testRev, 1), "uuid-2"),
	})
	if status2.LastConverged.ReleaseID != "uuid-2" {
		t.Fatalf("expected releaseID uuid-2, got %q", status2.LastConverged.ReleaseID)
	}
}

func TestLastConvergedAdvancesOnNewRevision(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.RevisionAnnotation] = "rev-2"
	stack.Annotations[v1alpha1.ReleaseIDAnnotation] = "uuid-2"
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "rev-1", ReleaseID: "uuid-1", At: metav1.Now()}
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		withReleaseID(readyChild("web", "rev-2", "rev-2", 1), "uuid-2"),
	})
	assertPhase(t, status, v1alpha1.StackReady)
	if status.LastConverged.Revision != "rev-2" || status.LastConverged.ReleaseID != "uuid-2" {
		t.Fatalf("lastConverged should advance to rev-2/uuid-2, got %+v", status.LastConverged)
	}
}

func TestLastConvergedStickyThroughStalled(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev, ReleaseID: "uuid-1", At: metav1.Now()}
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackFailed)
	if status.LastConverged == nil || status.LastConverged.Revision != testRev {
		t.Fatalf("lastConverged must survive a stall, got %+v", status.LastConverged)
	}
}

// ---------------------------------------------------------------------------
// Release-ID gate
// ---------------------------------------------------------------------------

func TestReleaseIDGatePreventsEarlyConvergence(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.ReleaseIDAnnotation] = "uuid-2"
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		withReleaseID(readyChild("web", testRev, testRev, 1), "uuid-1"),
	})
	assertPhase(t, status, v1alpha1.StackProgressing)
	if status.LastConverged != nil {
		t.Fatal("lastConverged must not be written when child carries a stale release-id")
	}
	if status.Resources[0].Message != "converged on a previous release; waiting for current release" {
		t.Fatalf("expected release-id mismatch message, got %q", status.Resources[0].Message)
	}
}

func TestReleaseIDGatePassesWhenStackHasNoReleaseID(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	assertPhase(t, status, v1alpha1.StackReady)
}

func TestReleaseIDGateMatchingIDs(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.ReleaseIDAnnotation] = "uuid-1"
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		withReleaseID(readyChild("web", testRev, testRev, 1), "uuid-1"),
	})
	assertPhase(t, status, v1alpha1.StackReady)
	if status.LastConverged == nil || status.LastConverged.ReleaseID != "uuid-1" {
		t.Fatalf("expected lastConverged with releaseID uuid-1, got %+v", status.LastConverged)
	}
}

func TestStandaloneLastConvergedNeverWritten(t *testing.T) {
	stack := standaloneStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "leftover", At: metav1.Now()}
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", "", "", 1),
	})
	assertPhase(t, status, v1alpha1.StackReady)
	if status.LastConverged == nil || status.LastConverged.Revision != "leftover" {
		t.Fatal("standalone mode must carry forward but never overwrite lastConverged")
	}
}

// ---------------------------------------------------------------------------
// Summary content & ordering
// ---------------------------------------------------------------------------

func TestSummaryFieldsPropagated(t *testing.T) {
	c := readyChild("web", testRev, testRev, 1)
	c.Status.Replicas = 3
	c.Status.AvailableReplicas = 2
	c.Status.UpdatedReplicas = 1
	c.Status.LastConverged = &v1alpha1.StackResourceConvergenceRecord{Revision: testRev, At: metav1.Now()}
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{c})
	s := status.Resources[0]
	if s.Replicas != 3 || s.AvailableReplicas != 2 || s.UpdatedReplicas != 1 {
		t.Fatalf("replicas not propagated")
	}
	if s.ConvergedRevision != testRev {
		t.Fatalf("convergedRevision not set for converged managed child")
	}
}

func TestSummaryOrderFollowsSpec(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "api", "worker"), []v1alpha1.StackResource{
		readyChild("worker", testRev, testRev, 1),
		readyChild("web", testRev, testRev, 1),
	})
	got := make([]string, 0, len(status.Resources))
	for _, r := range status.Resources {
		got = append(got, r.Name)
	}
	if !slices.Equal(got, []string{"web", "api", "worker"}) {
		t.Fatalf("summary order = %v, want [web api worker]", got)
	}
	if !status.Resources[1].Missing {
		t.Fatal("api (index 1) should be the missing resource")
	}
}

func TestServingNotConvergedSummaryMessage(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		servingNotConvergedChild("web", testRev, testRev, 1),
	})
	if status.Resources[0].Message != "serving traffic but not fully converged" {
		t.Fatalf("expected serving-not-converged message, got %q", status.Resources[0].Message)
	}
}

func TestStalledSummaryMessage(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
	})
	if status.Resources[0].Message != "resource is stalled: WorkloadTypeNotSupported" {
		t.Fatalf("unexpected stalled summary message: %q", status.Resources[0].Message)
	}
}

func TestStaleEchoMessage(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, "rev-0", 1),
	})
	if status.Resources[0].Message != "ready on a previous revision; latest revision not yet observed" {
		t.Fatalf("unexpected message: %q", status.Resources[0].Message)
	}
}

func TestTargetRevisionSetWhileProgressing(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})
	if status.TargetRevision != testRev {
		t.Fatalf("TargetRevision must be set even while Progressing, got %q", status.TargetRevision)
	}
	assertPhase(t, status, v1alpha1.StackProgressing)
}

// ---------------------------------------------------------------------------
// Standalone mode
// ---------------------------------------------------------------------------

func TestStandaloneIgnoresRevisionMismatch(t *testing.T) {
	c := readyChild("web", "rev-x", "rev-y", 1)
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{c})
	assertPhase(t, status, v1alpha1.StackReady)
	if status.Resources[0].ConvergedRevision != "" {
		t.Fatalf("ConvergedRevision must be empty in standalone mode, got %q", status.Resources[0].ConvergedRevision)
	}
}

func TestStandaloneOrphanBlocksAvailable(t *testing.T) {
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{
		readyChild("web", "", "", 1),
		readyChild("leftover", "", "", 1),
	})
	assertPhase(t, status, v1alpha1.StackPending)
	assertCondition(t, status, string(v1alpha1.StackConditionResourcesReady), metav1.ConditionTrue)
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if avail.Reason != "OrphanedResources" {
		t.Fatalf("expected OrphanedResources, got %q", avail.Reason)
	}
}

func TestStandaloneMissing(t *testing.T) {
	status := aggregateStackStatus(standaloneStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", "", "", 1),
	})
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if avail.Reason != "MissingResources" {
		t.Fatalf("expected MissingResources, got %q", avail.Reason)
	}
}

func TestMultipleStalledListedSorted(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "api"), []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
		stalledChild("api", testRev, 1),
	})
	stalled := findCond(t, status.Conditions, string(v1alpha1.StackConditionStalled))
	if stalled.Message != "stalled resources: api, web" {
		t.Fatalf("expected sorted stalled list, got %q", stalled.Message)
	}
}
