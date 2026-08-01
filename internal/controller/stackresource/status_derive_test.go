package stackresource

import (
	"context"
	"strings"
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
	got := summaryCond(r, v1alpha1.StackResourceStatusAvailable)
	if got.Status != metav1.ConditionTrue || got.Reason != "ServingButStalled" {
		t.Fatalf("Available = %+v, want True/ServingButStalled", got)
	}
	// The serving rollup must still carry the diagnosis, not generic text.
	if !strings.Contains(got.Message, "terminal failure") || !strings.Contains(got.Message, "port 9090 closed") {
		t.Fatalf("Available message = %q, want it to name the terminal failure and the verdict message", got.Message)
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

// Rows 4/5 guard: with no verdicts filed, Available mirrors the
// WorkloadAvailable fact. An absent (or stale) fact is not health — the
// summary must not invent it. Phase still follows the converged fact.
func TestDeriveNoVerdictWithoutWorkloadAvailable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stale bool
	}{
		{name: "condition absent"},
		{name: "condition stale", stale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := deriveTestResource()
			setResourceCondition(r, v1alpha1.StackResourceWorkloadConverged, true, "DeploymentConverged", "rollout settled")
			if tc.stale {
				setResourceCondition(r, v1alpha1.StackResourceWorkloadAvailable, true, "DeploymentServing", "serving")
				meta.FindStatusCondition(r.Status.Conditions, string(v1alpha1.StackResourceWorkloadAvailable)).ObservedGeneration = 2
			}

			deriveSummaryStatus(r, &controller.VerdictCollector{})

			if got := summaryCond(r, v1alpha1.StackResourceStatusAvailable); got.Status != metav1.ConditionFalse || got.Reason != "WorkloadNotAvailable" {
				t.Fatalf("Available = %+v, want False/WorkloadNotAvailable", got)
			}
			if r.Status.Phase != v1alpha1.StackResourcePhaseReady {
				t.Fatalf("Phase = %s, want Ready (still follows the converged fact)", r.Status.Phase)
			}
			if got := summaryCond(r, v1alpha1.StackResourceStalled); got.Status != metav1.ConditionFalse {
				t.Fatalf("Stalled = %+v, want False (nothing terminal happened)", got)
			}
		})
	}
}

// ImageSourceRevision names the revision that is actually serving, so a pass
// that ends in a verdict must not stamp the revision the spec merely points at.
func TestDeriveFailedDoesNotStampImageSourceRevision(t *testing.T) {
	r := deriveTestResource()
	r.Spec.BuildSpec = &v1alpha1.StackResourceBuildSpec{
		SourceRevision: v1alpha1.SourceRevisionSpec{Volume: &v1alpha1.VolumeRevision{RevisionString: "new-rev"}},
	}
	r.Status.ImageSourceRevision = "serving-rev"
	c := &controller.VerdictCollector{}
	c.ReportFailed("BuildFailed", "image build failed terminally")

	deriveSummaryStatus(r, c)

	if r.Status.ImageSourceRevision != "serving-rev" {
		t.Fatalf("ImageSourceRevision = %q, want the previously serving revision preserved", r.Status.ImageSourceRevision)
	}
}

// The happy path does stamp it: the pass ran clean, so the spec's revision is
// the one serving.
func TestDeriveNoVerdictStampsImageSourceRevision(t *testing.T) {
	r := deriveTestResource()
	r.Spec.BuildSpec = &v1alpha1.StackResourceBuildSpec{
		SourceRevision: v1alpha1.SourceRevisionSpec{Volume: &v1alpha1.VolumeRevision{RevisionString: "new-rev"}},
	}
	setResourceCondition(r, v1alpha1.StackResourceWorkloadAvailable, true, "DeploymentServing", "serving")
	setResourceCondition(r, v1alpha1.StackResourceWorkloadConverged, true, "DeploymentConverged", "rollout settled")

	deriveSummaryStatus(r, &controller.VerdictCollector{})

	if r.Status.ImageSourceRevision != "new-rev" {
		t.Fatalf("ImageSourceRevision = %q, want new-rev", r.Status.ImageSourceRevision)
	}
}

// stubSubReconciler lets the test compose an arbitrary chain.
type stubSubReconciler struct {
	fn func(ctx context.Context, r *v1alpha1.StackResource) (subReconcilerResult, error)
}

func (s stubSubReconciler) reconcile(ctx context.Context, r *v1alpha1.StackResource) (subReconcilerResult, error) {
	return s.fn(ctx, r)
}

// A Failed verdict filed early in the chain must survive a later sub-reconciler
// running its full happy path. Pre-refactor this ended
// Available=True/Stalled=False/Phase=Degraded with the verdict erased.
func TestFailedVerdictSurvivesFullPass(t *testing.T) {
	r := deriveTestResource()
	reporter := defaultStatusReporter{}

	// Mirrors the workload serving branch + reportPortNotListening.
	portFailure := stubSubReconciler{fn: func(ctx context.Context, res *v1alpha1.StackResource) (subReconcilerResult, error) {
		reporter.SetCondition(res, v1alpha1.StackResourceWorkloadAvailable, true, "DeploymentServing", "serving")
		reporter.SetCondition(res, v1alpha1.StackResourceWorkloadConverged, false, "PortNotListening", "port 9090 closed")
		reporter.ReportFailed(ctx, res, "PortNotListening", "port 9090 closed")
		return resultContinue, nil
	}}
	// Mirrors the svc reconciler post-refactor: manages its objects, sets its
	// domain condition, reports nothing about the summary.
	svcLike := stubSubReconciler{fn: func(ctx context.Context, res *v1alpha1.StackResource) (subReconcilerResult, error) {
		reporter.SetCondition(res, v1alpha1.StackResourceIngressReady, true, "IngressConfigured", "routes configured")
		return resultNil, nil
	}}

	rec := &StackResourceReconciler{subReconcilers: []subReconciler{portFailure, svcLike}}
	ctx, verdicts := controller.ContextWithVerdicts(context.Background())
	if _, err := rec.reconcile(ctx, r); err != nil {
		t.Fatal(err)
	}
	deriveSummaryStatus(r, verdicts)

	if r.Status.Phase != v1alpha1.StackResourcePhaseFailed {
		t.Fatalf("Phase = %s, want Failed — a later sub-reconciler must not soften a Failed verdict", r.Status.Phase)
	}
	if got := summaryCond(r, v1alpha1.StackResourceStalled); got.Status != metav1.ConditionTrue || got.Reason != "PortNotListening" {
		t.Fatalf("Stalled = %+v, want True/PortNotListening", got)
	}
	if got := summaryCond(r, v1alpha1.StackResourceStatusAvailable); got.Status != metav1.ConditionTrue || got.Reason != "ServingButStalled" {
		t.Fatalf("Available = %+v, want True/ServingButStalled (serving fact preserved)", got)
	}
	if got := summaryCond(r, v1alpha1.StackResourceConverged); got.Status != metav1.ConditionFalse || got.Reason != "PortNotListening" {
		t.Fatalf("Converged = %+v, want False/PortNotListening", got)
	}
	if got := meta.FindStatusCondition(r.Status.Conditions, string(v1alpha1.StackResourceIngressReady)); got == nil || got.Status != metav1.ConditionTrue {
		t.Fatalf("IngressReady = %+v — later sub-reconcilers must still do their domain work", got)
	}
}
