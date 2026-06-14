package stack

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

const testRev = "rev-1"

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

func TestServingButNotConvergedIsNotConverged(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		servingNotConvergedChild("web", testRev, testRev, 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("a serving-but-not-converged child must keep the stack Pending, got %s", status.Phase)
	}
	if status.LastConverged != nil {
		t.Fatal("lastConverged must not be written when the child is only serving, not converged")
	}
}

func TestManagedAllConverged(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		readyChild("worker", testRev, testRev, 1),
	})
	if status.Phase != v1alpha1.StackReady {
		t.Fatalf("expected Ready, got %s", status.Phase)
	}
	if status.LastConverged == nil || status.LastConverged.Revision != testRev {
		t.Fatalf("expected lastConverged.revision=%q, got %+v", testRev, status.LastConverged)
	}
	if status.TargetRevision != testRev {
		t.Fatalf("expected targetRevision=%q, got %q", testRev, status.TargetRevision)
	}
}

func TestManagedStaleRevisionNotConverged(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, "rev-0", 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending for stale revision, got %s", status.Phase)
	}
	if status.LastConverged != nil {
		t.Fatal("lastConverged must not be written before convergence")
	}
}

func TestManagedUnannotatedChildBlocks(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", "", "", 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending for unannotated child, got %s", status.Phase)
	}
	if status.Resources[0].Message != "no revision annotation (required for release-managed stacks)" {
		t.Fatalf("unexpected message: %q", status.Resources[0].Message)
	}
}

func TestStandaloneConvergesOnReadyAlone(t *testing.T) {
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{
		readyChild("web", "", "", 1),
	})
	if status.Phase != v1alpha1.StackReady {
		t.Fatalf("expected Ready in standalone mode, got %s", status.Phase)
	}
	if status.LastConverged != nil {
		t.Fatal("lastConverged must not be set in standalone mode")
	}
	if status.TargetRevision != "" {
		t.Fatalf("targetRevision must be empty in standalone, got %q", status.TargetRevision)
	}
}

func TestStaleAvailableConditionExcluded(t *testing.T) {
	child := readyChild("web", testRev, testRev, 1)
	child.Generation = 2 // spec bumped but condition still reports generation 1
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{child})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending when Available condition is stale, got %s", status.Phase)
	}
	if status.Resources[0].Message != "no current status from controller" {
		t.Fatalf("unexpected message: %q", status.Resources[0].Message)
	}
}

func TestStalledChildPropagates(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
	})
	if status.Phase != v1alpha1.StackFailed {
		t.Fatalf("expected Failed for stalled child, got %s", status.Phase)
	}
	stalled := findCond(t, status.Conditions, string(v1alpha1.StackConditionStalled))
	if stalled.Status != metav1.ConditionTrue {
		t.Fatal("Stack Stalled should be True when a child is stalled")
	}
}

func TestStaleStalledIgnored(t *testing.T) {
	child := stalledChild("web", testRev, 1)
	child.Generation = 2 // spec was fixed, stalled verdict is now stale
	child.Status.Conditions = append(child.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceStatusAvailable),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: 2,
		Reason:             "Reconciling",
	})
	// Stalled condition still at gen 1 (stale), Available at gen 2 (fresh but false)
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{child})
	if status.Phase == v1alpha1.StackFailed {
		t.Fatal("stale Stalled condition should not cause Failed")
	}
	stalled := findCond(t, status.Conditions, string(v1alpha1.StackConditionStalled))
	if stalled.Status != metav1.ConditionFalse {
		t.Fatal("Stack Stalled should be False when child Stalled is stale")
	}
}

func TestLastConvergedSticky(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{
		Revision: "old-rev",
		At:       metav1.Now(),
	}
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending, got %s", status.Phase)
	}
	if status.LastConverged == nil || status.LastConverged.Revision != "old-rev" {
		t.Fatalf("lastConverged must be sticky; expected old-rev, got %+v", status.LastConverged)
	}
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if ready.Status != metav1.ConditionFalse {
		t.Fatal("ResourcesReady must be False for pending child")
	}
	if ready.Reason != "ResourcesNotReady" {
		t.Fatalf("expected reason ResourcesNotReady, got %q", ready.Reason)
	}
}

func TestWriteOnceGuard(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.ReleaseIDAnnotation] = "uuid-1"
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	if status.LastConverged == nil {
		t.Fatal("expected lastConverged to be set")
	}
	firstAt := status.LastConverged.At

	// Second reconcile with same target — At must not change.
	stack.Status.LastConverged = status.LastConverged
	status2 := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	if !status2.LastConverged.At.Equal(&firstAt) {
		t.Fatal("write-once guard failed: At changed on same target")
	}
}

func TestRollbackProducesNewRecord(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.ReleaseIDAnnotation] = "uuid-1"
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})

	// Rollback: same revision, new releaseID.
	stack.Status.LastConverged = status.LastConverged
	stack.Annotations[v1alpha1.ReleaseIDAnnotation] = "uuid-2"
	status2 := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	if status2.LastConverged.ReleaseID != "uuid-2" {
		t.Fatalf("expected releaseID uuid-2, got %q", status2.LastConverged.ReleaseID)
	}
}

func TestMissingChild(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending, got %s", status.Phase)
	}
	if !status.Resources[1].Missing {
		t.Fatal("expected worker to be Missing")
	}
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if ready.Reason != "MissingResources" {
		t.Fatalf("expected reason MissingResources, got %q", ready.Reason)
	}
}

// TestMissingChildFailsReadinessByCount locks in that readiness is driven by
// convergedCount == len(spec.resourceNames): a missing desired resource makes
// the stack not-ready even though every resource that EXISTS is converged.
// This must hold without relying on a separate missing-count guard, so the
// fragile "convergedCount == desiredExisting" form can never creep back.
func TestMissingChildFailsReadinessByCount(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1), // the only existing child, fully converged
	})
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if ready.Status != metav1.ConditionFalse {
		t.Fatal("ResourcesReady must be False when a desired resource is missing, even if all existing ones are converged")
	}
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if avail.Status != metav1.ConditionFalse {
		t.Fatal("Available must be False with a missing resource")
	}
}

func TestOrphanBlocksAvailableNotResourcesReady(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		readyChild("old-worker", testRev, testRev, 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending with orphan, got %s", status.Phase)
	}
	if !slices.Contains(status.OrphanedResources, "old-worker") {
		t.Fatalf("expected orphan, got %v", status.OrphanedResources)
	}
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if ready.Status != metav1.ConditionTrue {
		t.Fatal("ResourcesReady should be True")
	}
	if avail.Status != metav1.ConditionFalse {
		t.Fatal("Available should be False")
	}
}

func TestEmptyResourceNames(t *testing.T) {
	status := aggregateStackStatus(managedStack(), nil)
	if status.Phase != v1alpha1.StackReady {
		t.Fatalf("empty desired set should be vacuously Ready, got %s", status.Phase)
	}
}

func TestStandalonePendingChild(t *testing.T) {
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{
		pendingChild("web", "", 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending in standalone with unavailable child, got %s", status.Phase)
	}
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if avail.Status != metav1.ConditionFalse {
		t.Fatal("Available should be False")
	}
}

func TestMixedConvergedAndStalled(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		stalledChild("worker", testRev, 1),
	})
	if status.Phase != v1alpha1.StackFailed {
		t.Fatalf("expected Failed with one stalled child, got %s", status.Phase)
	}
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if ready.Status != metav1.ConditionFalse {
		t.Fatal("ResourcesReady should be False when any child is stalled")
	}
	stalled := findCond(t, status.Conditions, string(v1alpha1.StackConditionStalled))
	if stalled.Status != metav1.ConditionTrue {
		t.Fatal("Stack Stalled should be True")
	}
}

func TestMultipleMissingChildren(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "api", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending, got %s", status.Phase)
	}
	missingCount := 0
	for _, r := range status.Resources {
		if r.Missing {
			missingCount++
		}
	}
	if missingCount != 2 {
		t.Fatalf("expected 2 missing resources, got %d", missingCount)
	}
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if avail.Reason != "MissingResources" {
		t.Fatalf("expected reason MissingResources, got %q", avail.Reason)
	}
}

func TestOrphanPlusMissing(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		readyChild("stale-svc", testRev, testRev, 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending, got %s", status.Phase)
	}
	if !slices.Contains(status.OrphanedResources, "stale-svc") {
		t.Fatal("expected stale-svc in orphans")
	}
	hasMissing := false
	for _, r := range status.Resources {
		if r.Name == "worker" && r.Missing {
			hasMissing = true
		}
	}
	if !hasMissing {
		t.Fatal("expected worker to be Missing")
	}
}

func TestStalledClearsOnRecovery(t *testing.T) {
	stack := managedStack("web")
	// Simulate previous stalled state carried forward in conditions.
	meta.SetStatusCondition(&stack.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackConditionStalled),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: 2,
		Reason:             "ResourceStalled",
	})
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})
	if status.Phase != v1alpha1.StackReady {
		t.Fatalf("expected Ready after recovery, got %s", status.Phase)
	}
	stalled := findCond(t, status.Conditions, string(v1alpha1.StackConditionStalled))
	if stalled.Status != metav1.ConditionFalse {
		t.Fatal("Stalled should be cleared to False after recovery")
	}
}

func TestChildAvailableFalseMessage(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending, got %s", status.Phase)
	}
	if status.Resources[0].Message != "not available: Reconciling" {
		t.Fatalf("expected 'not available: Reconciling', got %q", status.Resources[0].Message)
	}
}

func TestStandaloneStalledChild(t *testing.T) {
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{
		stalledChild("web", "", 1),
	})
	if status.Phase != v1alpha1.StackFailed {
		t.Fatalf("expected Failed in standalone with stalled child, got %s", status.Phase)
	}
	stalled := findCond(t, status.Conditions, string(v1alpha1.StackConditionStalled))
	if stalled.Status != metav1.ConditionTrue {
		t.Fatal("Stalled should be True in standalone mode too")
	}
}

func TestStandaloneLastConvergedNeverWritten(t *testing.T) {
	stack := standaloneStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{
		Revision: "leftover",
		At:       metav1.Now(),
	}
	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", "", "", 1),
	})
	if status.Phase != v1alpha1.StackReady {
		t.Fatalf("expected Ready, got %s", status.Phase)
	}
	if status.LastConverged == nil || status.LastConverged.Revision != "leftover" {
		t.Fatal("standalone mode must carry forward but never overwrite lastConverged")
	}
}

// TestStalledOrphanDoesNotFailStack: a stalled resource NOT in
// spec.resourceNames (orphan, slated for deletion) must not drag the whole
// stack to Failed. Regression: the stalled count used to include orphans.
func TestStalledOrphanDoesNotFailStack(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		stalledChild("old-bad", testRev, 1), // orphan + stalled
	})
	if status.Phase == v1alpha1.StackFailed {
		t.Fatalf("stalled orphan must not fail the stack; got %s", status.Phase)
	}
	if !slices.Contains(status.OrphanedResources, "old-bad") {
		t.Fatalf("expected old-bad orphan, got %v", status.OrphanedResources)
	}
	stalled := findCond(t, status.Conditions, string(v1alpha1.StackConditionStalled))
	if stalled.Status != metav1.ConditionFalse {
		t.Fatal("Stack Stalled must be False when only an orphan is stalled")
	}
}

// TestMissingAndOrphanReportsMissing: when a desired resource is missing AND an
// orphan exists, the missing resource must be reported. Regression: the orphan
// case fired before the missing case and reported ResourcesReady=True, masking
// the missing resource.
func TestMissingAndOrphanReportsMissing(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		readyChild("old-db", testRev, testRev, 1), // orphan (worker is missing)
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending, got %s", status.Phase)
	}
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if ready.Status != metav1.ConditionFalse || ready.Reason != "MissingResources" {
		t.Fatalf("expected ResourcesReady False/MissingResources, got %s/%s", ready.Status, ready.Reason)
	}
	if !slices.Contains(status.OrphanedResources, "old-db") {
		t.Fatalf("orphan should still be listed, got %v", status.OrphanedResources)
	}
}

// TestUnhealthyOrphanDoesNotBlockResourcesReady: an orphan's health must not
// affect ResourcesReady — only desired resources count. Regression: convergence
// was counted over all children, so an unhealthy orphan flipped ResourcesReady
// to False and hid the orphan reason.
func TestUnhealthyOrphanDoesNotBlockResourcesReady(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		pendingChild("old-worker", testRev, 1), // orphan, not healthy
	})
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if ready.Status != metav1.ConditionTrue {
		t.Fatalf("ResourcesReady must be True regardless of orphan health, got %s", ready.Status)
	}
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if avail.Status != metav1.ConditionFalse || avail.Reason != "OrphanedResources" {
		t.Fatalf("expected Available False/OrphanedResources, got %s/%s", avail.Status, avail.Reason)
	}
}

// --- Summary content & ordering ---------------------------------------------

func TestSummaryFieldsPropagated(t *testing.T) {
	c := readyChild("web", testRev, testRev, 1)
	c.Status.Replicas = 3
	c.Status.AvailableReplicas = 2
	c.Status.UpdatedReplicas = 1
	c.Status.LastConverged = &v1alpha1.StackResourceConvergenceRecord{Revision: testRev, At: metav1.Now()}
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{c})

	s := status.Resources[0]
	if s.Replicas != 3 || s.AvailableReplicas != 2 || s.UpdatedReplicas != 1 {
		t.Fatalf("replicas not propagated: available=%d updated=%d total=%d", s.AvailableReplicas, s.UpdatedReplicas, s.Replicas)
	}
	if s.ObservedRevision != testRev {
		t.Fatalf("observedRevision not propagated: %q", s.ObservedRevision)
	}
	if s.ConvergedRevision != testRev {
		t.Fatalf("convergedRevision should be set for a converged managed child: %q", s.ConvergedRevision)
	}
	if s.LastConverged == nil || s.LastConverged.Revision != testRev {
		t.Fatalf("child lastConverged not propagated: %+v", s.LastConverged)
	}
}

func TestSummaryOrderFollowsSpec(t *testing.T) {
	// Children supplied out of order; summaries must follow spec.resourceNames
	// order (including the missing one inline) for a stable StatusHash.
	status := aggregateStackStatus(managedStack("web", "api", "worker"), []v1alpha1.StackResource{
		readyChild("worker", testRev, testRev, 1),
		readyChild("web", testRev, testRev, 1),
		// api is missing
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

// --- Per-child summary messages ---------------------------------------------

func TestStaleEchoMessage(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		readyChild("web", testRev, "rev-0", 1), // healthy but on an older revision
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending, got %s", status.Phase)
	}
	s := status.Resources[0]
	if s.Message != "ready on a previous revision; latest revision not yet observed" {
		t.Fatalf("unexpected message: %q", s.Message)
	}
	if s.ConvergedRevision != "" {
		t.Fatalf("ConvergedRevision must be empty for a stale child, got %q", s.ConvergedRevision)
	}
	if s.ObservedRevision != "rev-0" {
		t.Fatalf("ObservedRevision should reflect what the child applied (rev-0), got %q", s.ObservedRevision)
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

func TestChildUnknownStatusMessage(t *testing.T) {
	// Available=Unknown is the initial controller state; the summary must still
	// carry a diagnostic message (fallback), never an empty string.
	child := v1alpha1.StackResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "web",
			Generation:  1,
			Annotations: map[string]string{v1alpha1.RevisionAnnotation: testRev},
		},
		Status: v1alpha1.StackResourceStatus{
			Phase: v1alpha1.StackResourcePhasePending,
			Conditions: []metav1.Condition{{
				Type:               string(v1alpha1.StackResourceStatusAvailable),
				Status:             metav1.ConditionUnknown,
				ObservedGeneration: 1,
				Reason:             "Initializing",
			}},
		},
	}
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{child})
	if status.Resources[0].Message != "resource is not yet available" {
		t.Fatalf("unexpected message for Unknown status: %q", status.Resources[0].Message)
	}
}

// --- TargetRevision ---------------------------------------------------------

func TestTargetRevisionSetWhilePending(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})
	if status.TargetRevision != testRev {
		t.Fatalf("TargetRevision must be set even while Pending, got %q", status.TargetRevision)
	}
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending, got %s", status.Phase)
	}
}

// --- lastConverged transitions ----------------------------------------------

func TestLastConvergedAdvancesOnNewRevision(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.RevisionAnnotation] = "rev-2"
	stack.Annotations[v1alpha1.ReleaseIDAnnotation] = "uuid-2"
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "rev-1", ReleaseID: "uuid-1", At: metav1.Now()}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", "rev-2", "rev-2", 1),
	})
	if status.Phase != v1alpha1.StackReady {
		t.Fatalf("expected Ready, got %s", status.Phase)
	}
	if status.LastConverged.Revision != "rev-2" || status.LastConverged.ReleaseID != "uuid-2" {
		t.Fatalf("lastConverged should advance to rev-2/uuid-2, got %+v", status.LastConverged)
	}
}

func TestLastConvergedNotAdvancedMidDeploy(t *testing.T) {
	stack := managedStack("web")
	stack.Annotations[v1alpha1.RevisionAnnotation] = "rev-2"
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "rev-1", ReleaseID: "uuid-1", At: metav1.Now()}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", "rev-2", "rev-1", 1), // healthy but still on rev-1
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending mid-deploy, got %s", status.Phase)
	}
	if status.LastConverged.Revision != "rev-1" {
		t.Fatalf("lastConverged must stay rev-1 until the child converges, got %+v", status.LastConverged)
	}
}

func TestLastConvergedStickyThroughStalled(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev, ReleaseID: "uuid-1", At: metav1.Now()}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
	})
	if status.Phase != v1alpha1.StackFailed {
		t.Fatalf("expected Failed, got %s", status.Phase)
	}
	if status.LastConverged == nil || status.LastConverged.Revision != testRev {
		t.Fatalf("lastConverged must survive a stall, got %+v", status.LastConverged)
	}
}

// --- Priority & multiplicity ------------------------------------------------

func TestStalledBeatsMissing(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
		// worker missing
	})
	if status.Phase != v1alpha1.StackFailed {
		t.Fatalf("stalled should take priority over missing; got %s", status.Phase)
	}
	stalled := findCond(t, status.Conditions, string(v1alpha1.StackConditionStalled))
	if stalled.Status != metav1.ConditionTrue {
		t.Fatal("Stalled should be True")
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
	if avail.Message != "orphaned resources not in spec.resourceNames: alpha, zeta" {
		t.Fatalf("unexpected orphan message: %q", avail.Message)
	}
}

func TestEmptyDesiredWithOrphan(t *testing.T) {
	// No desired resources, but a leftover child exists → it is an orphan.
	status := aggregateStackStatus(managedStack(), []v1alpha1.StackResource{
		readyChild("leftover", testRev, testRev, 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending (orphan present), got %s", status.Phase)
	}
	if !slices.Contains(status.OrphanedResources, "leftover") {
		t.Fatalf("expected leftover orphan, got %v", status.OrphanedResources)
	}
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if ready.Status != metav1.ConditionTrue {
		t.Fatal("ResourcesReady should be vacuously True with no desired resources")
	}
}

// --- Standalone mode --------------------------------------------------------

func TestStandaloneIgnoresRevisionMismatch(t *testing.T) {
	// Standalone stacks do not fence on revision tokens, even if a child happens
	// to carry an annotation that disagrees with its observedRevision.
	c := readyChild("web", "rev-x", "rev-y", 1)
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{c})
	if status.Phase != v1alpha1.StackReady {
		t.Fatalf("standalone should converge on health alone, got %s", status.Phase)
	}
	if status.Resources[0].ConvergedRevision != "" {
		t.Fatalf("ConvergedRevision must be empty in standalone mode, got %q", status.Resources[0].ConvergedRevision)
	}
}

func TestStandaloneOrphanBlocksAvailable(t *testing.T) {
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{
		readyChild("web", "", "", 1),
		readyChild("leftover", "", "", 1),
	})
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending with orphan, got %s", status.Phase)
	}
	ready := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if ready.Status != metav1.ConditionTrue {
		t.Fatal("ResourcesReady should be True in standalone with a converged desired child")
	}
	avail := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailable))
	if avail.Status != metav1.ConditionFalse || avail.Reason != "OrphanedResources" {
		t.Fatalf("expected Available False/OrphanedResources, got %s/%s", avail.Status, avail.Reason)
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

// --- AvailableOnce condition ---------------------------------------------------

func TestAvailableOnceTrueWhenPreviouslyConverged(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "old-rev"}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})

	cond := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailableOnce))
	if cond.Status != metav1.ConditionTrue {
		t.Fatal("AvailableOnce should be True when stack has previously converged")
	}
}

func TestAvailableOnceNotSetOnFirstDeploy(t *testing.T) {
	status := aggregateStackStatus(managedStack("web"), []v1alpha1.StackResource{
		pendingChild("web", testRev, 1),
	})

	for _, c := range status.Conditions {
		if c.Type == string(v1alpha1.StackConditionAvailableOnce) {
			t.Fatal("AvailableOnce should not be set on first deploy (no LastConverged)")
		}
	}
}

func TestAvailableOnceTrueWhenAvailable(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})

	cond := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailableOnce))
	if cond.Status != metav1.ConditionTrue {
		t.Fatal("AvailableOnce should be True when stack has previously converged, even when currently healthy")
	}
}

func TestAvailableOnceTrueWhenStalledAndPreviouslyConverged(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "old-rev"}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		stalledChild("web", testRev, 1),
	})

	cond := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailableOnce))
	if cond.Status != metav1.ConditionTrue {
		t.Fatal("AvailableOnce should be True when stack has previously converged, even when stalled")
	}
}

func TestAvailableOnceTrueWithOrphans(t *testing.T) {
	stack := managedStack("web")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: testRev}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		readyChild("orphan", testRev, testRev, 1),
	})

	cond := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailableOnce))
	if cond.Status != metav1.ConditionTrue {
		t.Fatal("AvailableOnce should be True when stack has previously converged")
	}
}

func TestAvailableOnceTrueWhenMissingAndPreviouslyConverged(t *testing.T) {
	stack := managedStack("web", "worker")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "old-rev"}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
	})

	cond := findCond(t, status.Conditions, string(v1alpha1.StackConditionAvailableOnce))
	if cond.Status != metav1.ConditionTrue {
		t.Fatal("AvailableOnce should be True when stack has previously converged, even with missing children")
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

func TestServingNotConvergedStandaloneSummaryMessage(t *testing.T) {
	status := aggregateStackStatus(standaloneStack("web"), []v1alpha1.StackResource{
		servingNotConvergedChild("web", "", "", 1),
	})
	if status.Resources[0].Message != "serving traffic but not fully converged" {
		t.Fatalf("expected serving-not-converged message, got %q", status.Resources[0].Message)
	}
	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending, got %s", status.Phase)
	}
}

func TestServingNotConvergedDoesNotAdvanceLastConverged(t *testing.T) {
	stack := managedStack("web", "worker")
	stack.Status.LastConverged = &v1alpha1.ConvergenceRecord{Revision: "old-rev"}

	status := aggregateStackStatus(stack, []v1alpha1.StackResource{
		servingNotConvergedChild("web", testRev, testRev, 1),
		readyChild("worker", testRev, testRev, 1),
	})

	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending when one child is only serving, got %s", status.Phase)
	}
	if status.LastConverged.Revision != "old-rev" {
		t.Fatalf("lastConverged should stay at old-rev, got %q", status.LastConverged.Revision)
	}
}

func TestServingNotConvergedMixedWithConverged(t *testing.T) {
	status := aggregateStackStatus(managedStack("web", "worker"), []v1alpha1.StackResource{
		readyChild("web", testRev, testRev, 1),
		servingNotConvergedChild("worker", testRev, testRev, 1),
	})

	if status.Phase != v1alpha1.StackPending {
		t.Fatalf("expected Pending, got %s", status.Phase)
	}
	if status.Resources[0].Message != "" {
		t.Fatalf("converged child should have no message, got %q", status.Resources[0].Message)
	}
	if status.Resources[1].Message != "serving traffic but not fully converged" {
		t.Fatalf("serving-not-converged child got wrong message: %q", status.Resources[1].Message)
	}

	cond := findCond(t, status.Conditions, string(v1alpha1.StackConditionResourcesReady))
	if cond.Status != metav1.ConditionFalse {
		t.Fatal("ResourcesReady should be False when one child is not converged")
	}
}
