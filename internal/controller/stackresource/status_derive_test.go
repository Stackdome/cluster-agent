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

// summaryOf derives and returns the rolled-up summary, failing if none was written.
func summaryOf(t *testing.T, r *v1alpha1.StackResource, c *controller.VerdictCollector) *v1alpha1.StackResourceStatusSummary {
	t.Helper()
	deriveSummaryStatus(r, c)
	if r.Status.Summary == nil {
		t.Fatal("summary not written")
	}
	return r.Status.Summary
}

func crashDetail() v1alpha1.LastFailureDetail {
	return v1alpha1.LastFailureDetail{
		Type:                   v1alpha1.FailureTypeRuntimeCrash,
		LastTerminationReason:  "CrashLoopBackOff",
		LastTerminationMessage: "back-off restarting",
	}
}

func TestSummaryBuildInProgress(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceBuildReady, false, "BuildNotReady", "application build is not yet ready")
	c := &controller.VerdictCollector{}
	c.ReportNotReady("ImageBuildInProgress", "Image build is still in progress")

	s := summaryOf(t, r, c)

	if s.State != v1alpha1.SummaryStateBuilding || s.Reason != "BuildNotReady" {
		t.Fatalf("want Building/BuildNotReady, got %s/%s", s.State, s.Reason)
	}
	if s.ObservedGeneration != r.Generation {
		t.Fatalf("want gen %d, got %d", r.Generation, s.ObservedGeneration)
	}
}

func TestSummaryFailedVerdict(t *testing.T) {
	r := deriveTestResource()
	c := &controller.VerdictCollector{}
	c.ReportFailed("InvalidSpec", "bad spec")

	s := summaryOf(t, r, c)

	if s.State != v1alpha1.SummaryStateFailed || s.Reason != "InvalidSpec" || s.Message != "bad spec" {
		t.Fatalf("want Failed/InvalidSpec/bad spec, got %s/%s/%s", s.State, s.Reason, s.Message)
	}
	if s.ObservedGeneration != r.Generation {
		t.Fatalf("want gen %d, got %d", r.Generation, s.ObservedGeneration)
	}
}

// A failed build is reported plainly; suppressing the duplicate is the hub's job.
func TestSummaryBuildFailedVerdict(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceBuildReady, false, v1alpha1.ReasonBuildFailed, "application build failed terminally")
	c := &controller.VerdictCollector{}
	c.ReportFailed(v1alpha1.ReasonBuildFailed, "ImageBuild x failed")

	s := summaryOf(t, r, c)

	if s.State != v1alpha1.SummaryStateFailed || s.Reason != v1alpha1.ReasonBuildFailed {
		t.Fatalf("want Failed/%s, got %s/%s", v1alpha1.ReasonBuildFailed, s.State, s.Reason)
	}
	if s.Message != "ImageBuild x failed" {
		t.Fatalf("Message = %q, want the verdict message", s.Message)
	}
}

func TestSummaryWaitingOnDependencies(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceDependenciesReady, false, "DependenciesNotReady", "waiting for mysql")
	c := &controller.VerdictCollector{}
	c.ReportNotReady("DependenciesNotReady", "waiting for mysql")

	s := summaryOf(t, r, c)

	if s.State != v1alpha1.SummaryStateWaiting || s.Reason != "DependenciesNotReady" {
		t.Fatalf("want Waiting/DependenciesNotReady, got %s/%s", s.State, s.Reason)
	}
	if s.Message != "waiting for mysql" {
		t.Fatalf("Message = %q, want the condition message", s.Message)
	}
}

func TestSummaryRuntimeCrash(t *testing.T) {
	r := deriveTestResource()
	r.Status.LastFailureDetails = []v1alpha1.LastFailureDetail{crashDetail()}
	c := &controller.VerdictCollector{}
	c.ReportNotReady("StackResourceDeploymentNotReady", "deployment is not ready")

	s := summaryOf(t, r, c)

	if s.State != v1alpha1.SummaryStateFailed || s.Reason != "CrashLoopBackOff" {
		t.Fatalf("want Failed/CrashLoopBackOff, got %s/%s", s.State, s.Reason)
	}
	if s.Message != "back-off restarting" {
		t.Fatalf("Message = %q, want the termination message", s.Message)
	}
}

// The build gate ran first, so the workload never rolled this pass: the crash
// entry belongs to the previous revision.
func TestSummaryBuildOutranksStaleCrash(t *testing.T) {
	r := deriveTestResource()
	r.Status.LastFailureDetails = []v1alpha1.LastFailureDetail{crashDetail()}
	setResourceCondition(r, v1alpha1.StackResourceBuildReady, false, "BuildNotReady", "building")
	c := &controller.VerdictCollector{}
	c.ReportNotReady("ImageBuildInProgress", "Image build is still in progress")

	s := summaryOf(t, r, c)

	if s.State != v1alpha1.SummaryStateBuilding || s.Reason != "BuildNotReady" {
		t.Fatalf("want Building/BuildNotReady, got %s/%s", s.State, s.Reason)
	}
}

func TestSummaryDeployingWithPortDial(t *testing.T) {
	r := deriveTestResource()
	r.Status.PortCheck = &v1alpha1.PortCheckStatus{
		Revision:           "rev-3",
		Status:             v1alpha1.PortCheckStatusTypeFailure,
		FailingPortNumbers: []int32{80},
	}
	c := &controller.VerdictCollector{}
	c.ReportNotReady("StackResourceDeploymentNotReady", "deployment is not ready")

	s := summaryOf(t, r, c)

	if s.State != v1alpha1.SummaryStateDeploying || s.Reason != v1alpha1.ReasonPortNotListening {
		t.Fatalf("want Deploying/%s, got %s/%s", v1alpha1.ReasonPortNotListening, s.State, s.Reason)
	}
	if s.Message != "port 80 not accepting connections" {
		t.Fatalf("Message = %q, want the port-dial diagnosis", s.Message)
	}
}

func TestSummaryDeployingVerdictFallback(t *testing.T) {
	r := deriveTestResource()
	c := &controller.VerdictCollector{}
	c.ReportNotReady("JobRunning", "job created, waiting for completion")

	s := summaryOf(t, r, c)

	if s.State != v1alpha1.SummaryStateDeploying || s.Reason != "JobRunning" {
		t.Fatalf("want Deploying/JobRunning, got %s/%s", s.State, s.Reason)
	}
	if s.Message != "job created, waiting for completion" {
		t.Fatalf("Message = %q, want the verdict message", s.Message)
	}
}

func TestSummaryPreviousRevisionSuffix(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceWorkloadAvailable, true, "DeploymentServing", "serving")
	r.Status.PortCheck = &v1alpha1.PortCheckStatus{
		Revision:           "rev-3",
		Status:             v1alpha1.PortCheckStatusTypeFailure,
		FailingPortNumbers: []int32{80},
	}
	c := &controller.VerdictCollector{}
	c.ReportNotReady("StackResourceDeploymentNotReady", "deployment is not ready")

	s := summaryOf(t, r, c)

	if s.State != v1alpha1.SummaryStateDeploying {
		t.Fatalf("State = %s, want Deploying", s.State)
	}
	if s.Message != "port 80 not accepting connections (previous revision still serving traffic)" {
		t.Fatalf("Message = %q, want the port-dial diagnosis plus the serving suffix", s.Message)
	}
}

func TestSummaryReady(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceWorkloadConverged, true, "FullyConverged", "all replicas updated")
	setResourceCondition(r, v1alpha1.StackResourceWorkloadAvailable, true, "DeploymentServing", "serving")

	s := summaryOf(t, r, &controller.VerdictCollector{})

	if s.State != v1alpha1.SummaryStateReady || s.Reason != "FullyConverged" {
		t.Fatalf("want Ready/FullyConverged, got %s/%s", s.State, s.Reason)
	}
	if s.Message != "all replicas updated" {
		t.Fatalf("Message = %q, want the converged message", s.Message)
	}
}

// Converged without a serving fact is not Ready: nothing asserted traffic flows.
func TestSummaryConvergedWithoutAvailableIsDeploying(t *testing.T) {
	r := deriveTestResource()
	setResourceCondition(r, v1alpha1.StackResourceWorkloadConverged, true, "FullyConverged", "all replicas updated")

	s := summaryOf(t, r, &controller.VerdictCollector{})

	if s.State != v1alpha1.SummaryStateDeploying {
		t.Fatalf("State = %s, want Deploying (never Ready without WorkloadAvailable)", s.State)
	}
	if s.Reason != "FullyConverged" {
		t.Fatalf("Reason = %s, want FullyConverged from the WorkloadConverged fallback", s.Reason)
	}
}

// A BuildReady=False from a previous generation describes a rollout that no
// longer exists, so it must not produce Building.
func TestSummaryStaleConditionsIgnored(t *testing.T) {
	r := deriveTestResource()
	meta.SetStatusCondition(&r.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceBuildReady),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: r.Generation - 1,
		Reason:             "BuildNotReady",
		Message:            "application build is not yet ready",
	})
	c := &controller.VerdictCollector{}
	c.ReportNotReady("StackResourceDeploymentNotReady", "deployment is not ready")

	s := summaryOf(t, r, c)

	if s.State != v1alpha1.SummaryStateDeploying {
		t.Fatalf("State = %s, want Deploying (a stale condition must not produce Building)", s.State)
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
