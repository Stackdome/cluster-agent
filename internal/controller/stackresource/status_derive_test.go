package stackresource

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

func deriveTestResource() *v1alpha1.StackResource {
	return &v1alpha1.StackResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "derive-test", Namespace: "ns", Generation: 3,
			Annotations: map[string]string{v1alpha1.RevisionAnnotation: "rev-3"},
		},
	}
}

func summaryCond(r *v1alpha1.StackResource, t v1alpha1.StackResourceStatusCondition) *metav1.Condition {
	return meta.FindStatusCondition(r.Status.Conditions, string(t))
}

// Matrix row 1: Failed verdict + serving workload.
func TestDeriveFailedWhileServing(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceWorkloadAvailable, true, "DeploymentServing", "serving")
	setResourceCondition(r, v1alpha1.StackResourceWorkloadConverged, false, "PortNotListening", "port 9090 closed")
	c := &controller.VerdictCollector{}
	c.ReportFailed("PortNotListening", "port 9090 closed")

	deriveSummaryStatus(r, c)

	if r.Status.Phase != v1alpha1.StackResourcePhaseFailed {
		t.Fatalf("Phase = %s, want Failed", r.Status.Phase)
	}
	if got := summaryCond(r, v1alpha1.StackResourceStalled); got.Status != metav1.ConditionTrue || got.Reason != "PortNotListening" {
		t.Fatalf("Stalled = %+v, want True/PortNotListening", got)
	}
	if got := summaryCond(r, v1alpha1.StackResourceStatusAvailable); got.Status != metav1.ConditionTrue || got.Reason != "ServingButStalled" {
		t.Fatalf("Available = %+v, want True/ServingButStalled", got)
	}
	if got := summaryCond(r, v1alpha1.StackResourceConverged); got.Status != metav1.ConditionFalse || got.Reason != "PortNotListening" {
		t.Fatalf("Converged = %+v, want False/PortNotListening", got)
	}
	if r.Status.ObservedGeneration != 3 || r.Status.ObservedRevision != "rev-3" {
		t.Fatalf("observed stamps wrong: gen=%d rev=%q", r.Status.ObservedGeneration, r.Status.ObservedRevision)
	}
}

// Matrix row 2: Failed verdict, workload not serving.
func TestDeriveFailedNotServing(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceWorkloadAvailable, false, "DeploymentNotAvailable", "not up")
	c := &controller.VerdictCollector{}
	c.ReportFailed("BuildFailed", "image build failed terminally")

	deriveSummaryStatus(r, c)

	if r.Status.Phase != v1alpha1.StackResourcePhaseFailed {
		t.Fatalf("Phase = %s, want Failed", r.Status.Phase)
	}
	if got := summaryCond(r, v1alpha1.StackResourceStatusAvailable); got.Status != metav1.ConditionFalse || got.Reason != "BuildFailed" {
		t.Fatalf("Available = %+v, want False/BuildFailed", got)
	}
	if got := summaryCond(r, v1alpha1.StackResourceStalled); got.Status != metav1.ConditionTrue {
		t.Fatalf("Stalled = %+v, want True", got)
	}
	if got := summaryCond(r, v1alpha1.StackResourceConverged); got.Status != metav1.ConditionFalse {
		t.Fatalf("Converged = %+v, want False", got)
	}
}

// Matrix row 2 guard: WorkloadAvailable=True from a PREVIOUS generation is not "serving".
func TestDeriveStaleWorkloadAvailableDoesNotCountAsServing(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceWorkloadAvailable, true, "DeploymentServing", "serving")
	meta.FindStatusCondition(r.Status.Conditions, string(v1alpha1.StackResourceWorkloadAvailable)).ObservedGeneration = 2
	c := &controller.VerdictCollector{}
	c.ReportFailed("InvalidSpec", "bad spec")

	deriveSummaryStatus(r, c)

	if got := summaryCond(r, v1alpha1.StackResourceStatusAvailable); got.Status != metav1.ConditionFalse {
		t.Fatalf("Available = %+v, want False for stale serving signal", got)
	}
}

// Matrix row 3: NotReady verdict.
func TestDeriveNotReady(t *testing.T) {
	r := deriveTestResource()
	c := &controller.VerdictCollector{}
	c.ReportNotReady("DependenciesNotReady", "waiting for db")

	deriveSummaryStatus(r, c)

	if r.Status.Phase != v1alpha1.StackResourcePhasePending {
		t.Fatalf("Phase = %s, want Pending", r.Status.Phase)
	}
	if got := summaryCond(r, v1alpha1.StackResourceStatusAvailable); got.Status != metav1.ConditionFalse || got.Reason != "DependenciesNotReady" {
		t.Fatalf("Available = %+v, want False/DependenciesNotReady", got)
	}
	if got := summaryCond(r, v1alpha1.StackResourceStalled); got.Status != metav1.ConditionFalse || got.Reason != "DependenciesNotReady" {
		t.Fatalf("Stalled = %+v, want False with current reason", got)
	}
	if got := summaryCond(r, v1alpha1.StackResourceConverged); got.Status != metav1.ConditionFalse || got.Reason != "DependenciesNotReady" {
		t.Fatalf("Converged = %+v, want False/DependenciesNotReady", got)
	}
}

// Rows 1/2 beat row 3 when both verdicts were reported.
func TestDeriveFailedBeatsNotReady(t *testing.T) {
	r := deriveTestResource()
	c := &controller.VerdictCollector{}
	c.ReportNotReady("DependenciesNotReady", "waiting")
	c.ReportFailed("JobFailed", "boom")

	deriveSummaryStatus(r, c)

	if r.Status.Phase != v1alpha1.StackResourcePhaseFailed {
		t.Fatalf("Phase = %s, want Failed (Failed beats NotReady)", r.Status.Phase)
	}
}

// Matrix row 4: no verdicts, workload converged.
func TestDeriveReadyConverged(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceWorkloadConverged, true, "DeploymentConverged", "rollout settled")
	setResourceCondition(r, v1alpha1.StackResourceWorkloadAvailable, true, "DeploymentServing", "serving")

	deriveSummaryStatus(r, &controller.VerdictCollector{})

	if r.Status.Phase != v1alpha1.StackResourcePhaseReady {
		t.Fatalf("Phase = %s, want Ready", r.Status.Phase)
	}
	if got := summaryCond(r, v1alpha1.StackResourceStatusAvailable); got.Status != metav1.ConditionTrue {
		t.Fatalf("Available = %+v, want True", got)
	}
	if got := summaryCond(r, v1alpha1.StackResourceStalled); got.Status != metav1.ConditionFalse {
		t.Fatalf("Stalled = %+v, want False", got)
	}
	if got := summaryCond(r, v1alpha1.StackResourceConverged); got.Status != metav1.ConditionTrue || got.Reason != "DeploymentConverged" {
		t.Fatalf("Converged = %+v, want True mirroring WorkloadConverged", got)
	}
	if r.Status.StatusHash == "" {
		t.Fatal("StatusHash must be stamped")
	}
}

// Rows 4/5 guard: a WorkloadConverged=True left over from a PREVIOUS
// generation must not lend its reason to the summary — the current rollout
// has simply not reported yet.
func TestDeriveStaleWorkloadConvergedReasonNotMirrored(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceWorkloadConverged, true, "DeploymentConverged", "rollout settled")
	meta.FindStatusCondition(r.Status.Conditions, string(v1alpha1.StackResourceWorkloadConverged)).ObservedGeneration = 2

	deriveSummaryStatus(r, &controller.VerdictCollector{})

	got := summaryCond(r, v1alpha1.StackResourceConverged)
	if got.Status != metav1.ConditionFalse || got.Reason != "WorkloadNotConverged" {
		t.Fatalf("Converged = %+v, want False/WorkloadNotConverged for a stale condition", got)
	}
}

// Matrix row 5: no verdicts, workload not converged.
func TestDeriveReadyNotConvergedIsDegraded(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceWorkloadConverged, false, "NotConverged", "rolling")
	setResourceCondition(r, v1alpha1.StackResourceWorkloadAvailable, true, "DeploymentServing", "serving")

	deriveSummaryStatus(r, &controller.VerdictCollector{})

	if r.Status.Phase != v1alpha1.StackResourcePhaseDegraded {
		t.Fatalf("Phase = %s, want Degraded", r.Status.Phase)
	}
	if got := summaryCond(r, v1alpha1.StackResourceConverged); got.Status != metav1.ConditionFalse || got.Reason != "NotConverged" {
		t.Fatalf("Converged = %+v, want False mirroring WorkloadConverged", got)
	}
}
