# Single-Writer Status Derivation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** No sub-reconciler writes the summary status (`Available`/`Stalled`/`Converged`/`Phase`) directly; each records verdicts and domain conditions, and one derivation step in `Reconcile` computes the summary — eliminating the last-writer-wins overwrite described in `docs/port-check-status-clobber.md`. The domain/summary boundary is enforced by the compiler, not review.

**Architecture:** Sub-reconcilers record pass-scoped verdicts (`NotReady` = retriable, `Failed` = terminal) into a collector carried on `context.Context`, and write *domain* conditions (`WorkloadAvailable`, `WorkloadConverged`, `BuildReady`, …) directly — each domain condition has exactly one writer that observes its fact firsthand. After the sub-reconciler chain finishes — including early `resultStop` exits — `Reconcile` calls a single `deriveSummaryStatus` that maps verdicts + domain facts to `Phase`, `Available`, `Stalled`, `Converged` and stamps `ObservedGeneration`/`ObservedRevision`/`StatusHash`. Same derivation pattern `internal/controller/stack/aggregate.go` uses one level up — there `Converged` is likewise derived from children, which is why StackResource `Converged` is summary here and the workload's firsthand rollout fact moves to a new domain condition `WorkloadConverged`.

**Tech Stack:** Go, controller-runtime, Ginkgo v2 + Gomega (existing unit suites), Kind-based integration suite (`make test-integration`, `FOCUS=` to filter).

## Global Constraints

- Summary condition **types** are API contract — `Available`, `Stalled`, `Converged` keep their names; the Stack aggregate (`internal/controller/stack/aggregate.go`) and the hub read them. Reasons/messages may change. `WorkloadConverged` is a new domain condition; conditions are schemaless list entries, no CRD regeneration needed.
- The port-check verdict is terminal **per revision**; a resource that is serving but has a terminal port failure must end a full pass with `Phase=Failed`, `Available=True`, `WorkloadAvailable=True`, `Converged=False (PortNotListening)`, `Stalled=True (PortNotListening)`.
- One name per concept: the reporter method, the collector method, and the verdict accessor all say `NotReady` (retriable) or `Failed` (terminal). `Pending` appears only as the API phase `StackResourcePhasePending` that a `NotReady` verdict derives to.
- The domain/summary boundary is **compiler-enforced**: domain condition constants get their own Go type (`StackResourceDomainCondition`); `SetCondition`/`setResourceCondition` accept only that type. Summary constants stay on `StackResourceStatusCondition` and are writable only via an unexported helper in `status_derive.go`. `SetCondition(resource, StackResourceStalled, ...)` must be a compile error.
- `volume_controller.go` reconciles a different CR and is **out of scope**.
- Existing requeue/`resultStop` semantics of every sub-reconciler are unchanged — this refactor only moves *status writing*, never control flow.
- Error returns from sub-reconcilers keep today's behavior: `Reconcile` returns the error before any status update.
- Run `go build ./... && go vet ./...` after every task; run the named test suites in the task.
- Do not commit; the user handles commits.

## Condition Classification

| Kind | Conditions | Writer | Go type |
|---|---|---|---|
| Domain | `WorkloadAvailable`, `WorkloadConverged` (new) | workload sub-reconciler | `StackResourceDomainCondition` |
| Domain | `BuildReady` | imageBuild sub-reconciler | `StackResourceDomainCondition` |
| Domain | `IngressReady`, `TLSConfigured` | svc sub-reconciler | `StackResourceDomainCondition` |
| Domain | `DependenciesReady` | workload sub-reconciler (gates) | `StackResourceDomainCondition` |
| Summary | `Available`, `Stalled`, `Converged` | `deriveSummaryStatus` only | `StackResourceStatusCondition` |

## Derivation Matrix

The complete contract of `deriveSummaryStatus`. Inputs: the pass's verdicts, plus two facts read from domain conditions at the **current generation** — `serving` = `WorkloadAvailable=True`; `workloadConverged` = `WorkloadConverged=True`.

| # | Verdict | serving | workloadConverged | Phase | Available | Stalled | Converged |
|---|---------|---------|-------------------|-------|-----------|---------|-----------|
| 1 | Failed | true | any | `Failed` | **True** (`ServingButStalled`, "workload serving; terminal failure: \<msg\>") | **True** (verdict reason/msg) | False (verdict reason/msg) |
| 2 | Failed | false | any | `Failed` | False (verdict reason/msg) | **True** (verdict reason/msg) | False (verdict reason/msg) |
| 3 | NotReady (no Failed) | any | any | `Pending` | False (verdict reason/msg) | False (verdict reason/msg) | False (verdict reason/msg) |
| 4 | none | true | true | `Ready` | True (`StackResourceAvailable`) — mirrors the `WorkloadAvailable` fact | False (same reason/msg as Available) | True (mirrors `WorkloadConverged` reason/msg) |
| 5 | none | true | false | `Degraded` | True (`StackResourceAvailable`, "workload serving; current rollout not converged") — mirrors the `WorkloadAvailable` fact | False (same reason/msg as Available) | False (mirrors `WorkloadConverged` reason/msg) |
| 4/5 | none | false | any | per `workloadConverged` (`Ready`/`Degraded`) | False (`WorkloadNotAvailable`, "workload is not available") — mirrors the `WorkloadAvailable` fact | False (same reason/msg as Available) | mirrors `WorkloadConverged` |

Notes:
- Failed beats NotReady when both were reported in one pass (rows 1/2 win over 3).
- Verdicts: any sub-reconciler may file any number; the pass keeps only the **first** NotReady and the **first** Failed — chain order is dependency order, so the earliest gate to fail is the root cause. Nothing can retract a filed verdict; escalation works because Failed is a separate slot.
- Row 1 is the motivating bug (serving workload, dead declared secondary port): the Stack aggregate sees `Stalled=True` → child counts as stalled → Stack `Phase=Failed`; the hub distinguishes "failed and down" from "failed but serving" via `Available`.
- A stale domain condition from a previous generation counts as false (rows 2/5, not 1/4).
- Summary `Converged` is re-stamped at the current generation on every pass, so consumers never see a stale value.
- **Invariant** rows 4/5 rely on: a sub-reconciler that leaves its domain not-OK must file a verdict in the same pass. "No verdicts" therefore means every gate passed and the workload reconciler ran to its serving branch. Enforced by review, pinned by the Task 5 chain test. Rows 4/5 do not *depend* on the invariant for `Available`: with no `WorkloadAvailable=True` at the current generation the summary reports `Available=False`, it never asserts health from an empty collector.
- `ImageSourceRevision` is stamped only in the no-verdict branch: it names the revision that is actually serving, not the revision the spec points at.

## File Structure

| File | Role |
|---|---|
| `api/core/v1alpha1/stack_resource_types.go` (modify) | `StackResourceDomainCondition` type split; add `WorkloadConverged` |
| `internal/controller/verdict.go` (create) | Verdict types, collector, context plumbing |
| `internal/controller/verdict_test.go` (create) | Collector unit tests |
| `internal/controller/stackresource/status_derive.go` (create) | `deriveSummaryStatus` + unexported summary-condition writer |
| `internal/controller/stackresource/status_derive_test.go` (create) | Table-driven derivation tests + chain-level overwrite spec |
| `internal/controller/stackresource/stackresource_controller.go` (modify) | Wire derivation into `Reconcile`; report helpers become verdict wrappers; `defaultStatusReporter` |
| `internal/controller/stackresource/svc_reconciler.go` (modify) | Delete `reportStackResourceReady` calls |
| `internal/controller/stackresource/registry_auth_reconciler.go` (modify) | Route through verdict wrappers |
| `internal/controller/stackresource/image_build_reconciler.go` (modify) | Route through verdict wrappers |
| `internal/controller/stackresource/workload/reconciler.go` (modify) | `StatusReporter` interface: add `ctx`, drop `ReportReady`, domain-typed `SetCondition` |
| `internal/controller/stackresource/workload/{deployment,statefulset,job,cronjob,readiness}.go` (modify) | `Converged` writes become `WorkloadConverged`; call sites gain `ctx`; `reportPortNotListening` rewritten |
| workload + stackresource `*_test.go` (modify) | Fake reporter signature, expectation updates |

---

### Task 1: Verdict collector in `internal/controller`

**Files:**
- Create: `internal/controller/verdict.go`
- Create: `internal/controller/verdict_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces (used by every later task):
  - `type Verdict struct { Reason, Message string }`
  - `type VerdictCollector struct { /* unexported */ }`
  - `func (c *VerdictCollector) ReportNotReady(reason, msg string)` — first NotReady wins
  - `func (c *VerdictCollector) ReportFailed(reason, msg string)` — first Failed wins
  - `func (c *VerdictCollector) NotReady() *Verdict` / `func (c *VerdictCollector) Failed() *Verdict` — nil when unset
  - `func ContextWithVerdicts(ctx context.Context) (context.Context, *VerdictCollector)`
  - `func VerdictsFromContext(ctx context.Context) *VerdictCollector` — never nil: returns a discard collector when absent, so unit tests that call sub-reconcilers with a bare context don't panic

- [ ] **Step 1: Write the failing test**

`internal/controller/verdict_test.go`:

```go
package controller

import (
	"context"
	"testing"
)

func TestVerdictCollectorFirstWins(t *testing.T) {
	c := &VerdictCollector{}
	if c.NotReady() != nil || c.Failed() != nil {
		t.Fatal("fresh collector must have no verdicts")
	}

	c.ReportNotReady("First", "first not-ready")
	c.ReportNotReady("Second", "second not-ready")
	if got := c.NotReady(); got == nil || got.Reason != "First" {
		t.Fatalf("first NotReady must win, got %+v", got)
	}

	c.ReportFailed("FirstFailed", "first failure")
	c.ReportFailed("SecondFailed", "second failure")
	if got := c.Failed(); got == nil || got.Reason != "FirstFailed" {
		t.Fatalf("first Failed must win, got %+v", got)
	}
}

func TestVerdictContextPlumbing(t *testing.T) {
	ctx, c := ContextWithVerdicts(context.Background())
	VerdictsFromContext(ctx).ReportFailed("Boom", "msg")
	if got := c.Failed(); got == nil || got.Reason != "Boom" {
		t.Fatalf("collector from context must be the attached one, got %+v", got)
	}

	// A bare context yields a usable discard collector, never nil.
	discard := VerdictsFromContext(context.Background())
	discard.ReportNotReady("Ignored", "msg") // must not panic
	if c.NotReady() != nil {
		t.Fatal("discard collector must not leak into the attached one")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestVerdict -v`
Expected: FAIL — `undefined: VerdictCollector`

- [ ] **Step 3: Write the implementation**

`internal/controller/verdict.go`:

```go
package controller

import "context"

// Verdict is a sub-reconciler's summary opinion for one reconcile pass.
type Verdict struct {
	Reason  string
	Message string
}

// VerdictCollector accumulates pass-scoped verdicts. First report of each
// kind wins: the chain runs in dependency order, so the earliest gate to
// fail is the root cause worth surfacing. Nothing can retract a verdict.
// Not safe for concurrent use; one collector belongs to one reconcile pass.
type VerdictCollector struct {
	notReady *Verdict
	failed   *Verdict
}

// ReportNotReady records a retriable not-ready verdict (derives to Phase=Pending).
func (c *VerdictCollector) ReportNotReady(reason, msg string) {
	if c.notReady == nil {
		c.notReady = &Verdict{Reason: reason, Message: msg}
	}
}

// ReportFailed records a terminal failure (derives to Phase=Failed, Stalled=True).
func (c *VerdictCollector) ReportFailed(reason, msg string) {
	if c.failed == nil {
		c.failed = &Verdict{Reason: reason, Message: msg}
	}
}

func (c *VerdictCollector) NotReady() *Verdict { return c.notReady }
func (c *VerdictCollector) Failed() *Verdict   { return c.failed }

type verdictCtxKey struct{}

// ContextWithVerdicts attaches a fresh collector for one reconcile pass.
func ContextWithVerdicts(ctx context.Context) (context.Context, *VerdictCollector) {
	c := &VerdictCollector{}
	return context.WithValue(ctx, verdictCtxKey{}, c), c
}

// VerdictsFromContext returns the pass's collector. Contexts without one get
// a discard collector so callers never nil-check (unit tests, stray callers).
func VerdictsFromContext(ctx context.Context) *VerdictCollector {
	if c, ok := ctx.Value(verdictCtxKey{}).(*VerdictCollector); ok {
		return c
	}
	return &VerdictCollector{}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestVerdict -v`
Expected: PASS

---

### Task 2: Domain/summary type split in the API + `StatusReporter` refit

**Files:**
- Modify: `api/core/v1alpha1/stack_resource_types.go:33-41` (condition constants)
- Modify: `internal/controller/stackresource/workload/reconciler.go:23-28` (interface)
- Modify: `internal/controller/stackresource/stackresource_controller.go:149-161` (`setResourceCondition`), `:291-302` (`defaultStatusReporter`)
- Modify — `Converged` writes become `WorkloadConverged` (same file, same reasons/messages):
  - `workload/deployment.go` (converged/not-converged writes near :120-122)
  - `workload/statefulset.go` (equivalent writes)
  - `workload/job.go:97` (`JobComplete`)
  - `workload/cronjob.go:64` (`CronJobScheduled`)
  - `workload/readiness.go:236` (temporary — rewritten in Task 4)
- Modify — call sites gain `ctx` (all already have `ctx` in scope):
  - `workload/reconciler.go:79,90` (`DependenciesNotReady`, `VolumeMountsNotReady`), `:~104` (`ApplicationBuildNotYetReady`)
  - `workload/deployment.go:46,79,174`
  - `workload/statefulset.go:28,47,117`
  - `workload/job.go:28,106,113`
- Modify: workload test fakes and assertions (`workload/helpers_test.go` and any `*_test.go` implementing `StatusReporter`)

**Interfaces:**
- Consumes: `controller.VerdictsFromContext` (Task 1).
- Produces — API types and the reporter interface every later task compiles against:

`api/core/v1alpha1/stack_resource_types.go` — split the constants (both types are string-kinded, so existing `string(...)` readers like the Stack aggregate compile unchanged):

```go
// StackResourceDomainCondition is a condition a sub-reconciler owns and
// writes directly — a firsthand fact, one writer per condition type.
type StackResourceDomainCondition string

const (
	StackResourceTLSConfigured     StackResourceDomainCondition = "TLSConfigured"
	StackResourceDependenciesReady StackResourceDomainCondition = "DependenciesReady"
	StackResourceBuildReady        StackResourceDomainCondition = "BuildReady"
	StackResourcePreDeployComplete StackResourceDomainCondition = "PreDeployComplete"
	StackResourceWorkloadAvailable StackResourceDomainCondition = "WorkloadAvailable"
	StackResourceWorkloadConverged StackResourceDomainCondition = "WorkloadConverged"
	StackResourceIngressReady      StackResourceDomainCondition = "IngressReady"
)

// StackResourceStatusCondition values are summary conditions — judgments
// across domains, written only by the status derivation step.
const (
	StackResourceStatusAvailable StackResourceStatusCondition = "Available"
	StackResourceStalled         StackResourceStatusCondition = "Stalled"
	StackResourceConverged       StackResourceStatusCondition = "Converged"
)
```

Reporter interface:

```go
type StatusReporter interface {
	// ReportNotReady records a retriable not-ready verdict for this pass.
	ReportNotReady(ctx context.Context, r *v1alpha1.StackResource, reason, msg string)
	// ReportFailed records a terminal failure verdict for this pass.
	ReportFailed(ctx context.Context, r *v1alpha1.StackResource, reason, msg string)
	// SetCondition writes a domain condition immediately. Summary conditions
	// are not representable here — that is the point.
	SetCondition(r *v1alpha1.StackResource, condType v1alpha1.StackResourceDomainCondition, ready bool, reason, msg string)
}
```

`ReportReady` is **deleted** from the interface: under derivation, "ready" is the absence of verdicts. (Nothing in the workload package calls it today; the svc reconciler's calls are removed in Task 4.)

- [ ] **Step 1: Apply the API type split** as shown above.

- [ ] **Step 2: Retype `setResourceCondition`** in `stackresource_controller.go` to take `v1alpha1.StackResourceDomainCondition`. Compile (`go build ./...`) and fix every caller the compiler flags:
  - Workload `Converged` writes (deployment, statefulset, job:97, cronjob:64, readiness:236) → change the constant to `StackResourceWorkloadConverged`, keep reasons/messages verbatim.
  - Any non-workload caller passing `Available`/`Stalled`/`Converged` is inside the three old report helpers — they are deleted in Task 4; for now change those helpers to use `meta.SetStatusCondition` inline so the package compiles.

- [ ] **Step 3: Change the interface and `defaultStatusReporter`:**

```go
type defaultStatusReporter struct{}

func (defaultStatusReporter) ReportNotReady(ctx context.Context, r *v1alpha1.StackResource, reason, msg string) {
	controller.VerdictsFromContext(ctx).ReportNotReady(reason, msg)
}

func (defaultStatusReporter) ReportFailed(ctx context.Context, r *v1alpha1.StackResource, reason, msg string) {
	controller.VerdictsFromContext(ctx).ReportFailed(reason, msg)
}

func (defaultStatusReporter) SetCondition(r *v1alpha1.StackResource, t v1alpha1.StackResourceDomainCondition, ready bool, reason, msg string) {
	setResourceCondition(r, t, ready, reason, msg)
}
```

(Task 4 swaps the two verdict bodies for the shared package wrappers once those exist.) The `r *v1alpha1.StackResource` parameter stays even though the default implementation ignores it: test fakes assert per-resource calls, and dropping it would churn every fake for no gain.

- [ ] **Step 4: Sweep the workload call sites.** Every `r.Status.ReportNotReady(resource, ...)` becomes `r.Status.ReportNotReady(ctx, resource, ...)`; same for `ReportFailed`. `SetCondition` calls only change where the constant moved to `WorkloadConverged`.

- [ ] **Step 5: Update the workload test fakes.** Locate the fake/spy `StatusReporter` used by `workload` tests (`helpers_test.go` or per-file). Update method signatures (`ctx`, `StackResourceDomainCondition`); delete its `ReportReady` method; keep its call-recording behavior so existing assertions still work. Test assertions naming the `Converged` condition on workload writes now name `WorkloadConverged`.

- [ ] **Step 6: Compile and run the suites**

Run: `go build ./... && go test ./internal/controller/stackresource/... -count=1`
Expected: PASS. (The stackresource package still writes summary conditions via the old report helpers at this point — removed in Task 4.)

---

### Task 3: `deriveSummaryStatus` — the single writer

**Files:**
- Create: `internal/controller/stackresource/status_derive.go`
- Create: `internal/controller/stackresource/status_derive_test.go`

**Interfaces:**
- Consumes: `controller.VerdictCollector` (Task 1); API types from Task 2; `setResourceCondition` for nothing — summary writes use the local unexported helper.
- Produces:
  - `func deriveSummaryStatus(resource *v1alpha1.StackResource, verdicts *controller.VerdictCollector)` — called from `Reconcile` in Task 4.
  - `func setSummaryCondition(resource *v1alpha1.StackResource, condType v1alpha1.StackResourceStatusCondition, status bool, reason, msg string)` — unexported, this file only.
  - `func domainConditionTrue(resource *v1alpha1.StackResource, condType v1alpha1.StackResourceDomainCondition) bool` — True at current generation.

The behavior is the **Derivation Matrix** above — implement exactly those five rows. Always: stamp `ObservedGeneration`, `ObservedRevision` (from `v1alpha1.RevisionAnnotation`), and `StatusHash` last. `ImageSourceRevision` (when `BuildSpec != nil`) is stamped only in the no-verdict branch — it names the serving revision.

- [ ] **Step 1: Write the failing tests** — one per matrix row plus the stale-generation guard; plain Go test (no envtest; this is pure status math):

`internal/controller/stackresource/status_derive_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/controller/stackresource/ -run TestDerive -v`
Expected: FAIL — `undefined: deriveSummaryStatus`

- [ ] **Step 3: Write the implementation**

`internal/controller/stackresource/status_derive.go`:

```go
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
```

Note: the old `isResourceConverged` helper in `stackresource_controller.go` is superseded by `domainConditionTrue(resource, StackResourceWorkloadConverged)`; delete it in Task 4 when its last caller (`reportStackResourceReady`) goes.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/controller/stackresource/ -run TestDerive -v`
Expected: PASS

---

### Task 4: Wire derivation into `Reconcile`; retire the old summary writers

**Files:**
- Modify: `internal/controller/stackresource/stackresource_controller.go` — `Reconcile` (:56-88), delete `reportStackResourceReady`/`reportStackResourceNotReady`/`reportStackResourceFailed` and `isResourceConverged` (:114-147, :163-238)
- Modify: `internal/controller/stackresource/svc_reconciler.go:33-73`
- Modify: `internal/controller/stackresource/registry_auth_reconciler.go:56,88,107`
- Modify: `internal/controller/stackresource/image_build_reconciler.go:77,86`
- Modify: `internal/controller/stackresource/workload/readiness.go:233-238` (`reportPortNotListening`) and its callers `deployment.go:147-151`, `statefulset.go:96-100`

**Interfaces:**
- Consumes: `deriveSummaryStatus` (Task 3), `controller.ContextWithVerdicts` (Task 1), `StatusReporter` (Task 2).
- Produces: package-level wrappers replacing the three old report helpers, delegating 1:1 to the same-named collector methods:

```go
func reportNotReady(ctx context.Context, reason, msg string)  // → VerdictsFromContext(ctx).ReportNotReady
func reportFailed(ctx context.Context, reason, msg string)    // → VerdictsFromContext(ctx).ReportFailed
```

- [ ] **Step 1: Attach the collector and call derivation in `Reconcile`** (`stackresource_controller.go:56-88`). The derivation runs after the chain **and** after `getImageBuildStatus`, immediately before the DeepEqual/update — so it also runs for early `resultStop` exits:

```go
originalStatus := stackResource.Status.DeepCopy()
ctx, verdicts := controller.ContextWithVerdicts(ctx)

res, err := r.reconcile(ctx, stackResource)
if err != nil {
	return ctrl.Result{}, err
}
applicationBuildStatus, err := r.getImageBuildStatus(ctx, stackResource)
if err != nil {
	return ctrl.Result{}, err
}
stackResource.Status.CurrentBuild = applicationBuildStatus

deriveSummaryStatus(stackResource, verdicts)

if !equality.Semantic.DeepEqual(originalStatus, &stackResource.Status) {
	...unchanged...
}
```

- [ ] **Step 2: Replace the three report helpers** in `stackresource_controller.go` with the thin wrappers (delete `reportStackResourceReady` entirely — no replacement, ready is the absence of verdicts; delete `isResourceConverged`; keep `setResourceCondition`):

```go
// reportNotReady records a retriable not-ready verdict for this pass.
// deriveSummaryStatus turns verdicts into the summary conditions.
func reportNotReady(ctx context.Context, reason, msg string) {
	controller.VerdictsFromContext(ctx).ReportNotReady(reason, msg)
}

// reportFailed records a terminal failure verdict for this pass.
func reportFailed(ctx context.Context, reason, msg string) {
	controller.VerdictsFromContext(ctx).ReportFailed(reason, msg)
}
```

Also switch `defaultStatusReporter.ReportNotReady/ReportFailed` (Task 2) to delegate to these wrappers so the collector lookup lives in one place.

- [ ] **Step 3: Update the stackresource-package call sites:**

- `svc_reconciler.go:37,49,62` — delete the three `reportStackResourceReady(resource)` lines (keep the surrounding returns; the Worker/Job/CronJob early branch becomes just `return resultNil, nil`).
- `svc_reconciler.go:71` — `reportStackResourceNotReady(resource, "ServiceNotReady", message)` → `reportNotReady(ctx, "ServiceNotReady", message)`.
- `registry_auth_reconciler.go:56,88,107` — `reportStackResourceNotReady(...)` → `reportNotReady(ctx, <same reason>, <same message>)`.
- `image_build_reconciler.go:77` — `reportStackResourceFailed(resource, "BuildFailed", ...)` → `reportFailed(ctx, "BuildFailed", <same message>)` (the `setResourceCondition(resource, BuildReady, false, ...)` line above it stays).
- `image_build_reconciler.go:86` — `reportStackResourceNotReady(resource, "ImageBuildInProgress", ...)` → `reportNotReady(ctx, "ImageBuildInProgress", <same message>)`.

- [ ] **Step 4: Rewrite `reportPortNotListening`** (`workload/readiness.go:233-238`) — the motivating bug. `WorkloadAvailable` keeps its serving truth (the True written in the serving branch stands); the failure rides `WorkloadConverged` + a Failed verdict:

```go
// The workload is serving its primary port but a declared secondary port
// stayed closed past the grace window. Terminal per revision.
// WorkloadAvailable keeps the serving truth; the failure rides
// WorkloadConverged and the Failed verdict, which derivation turns into
// Phase=Failed / Stalled=True / Available=True("ServingButStalled").
func (r *Reconciler) reportPortNotListening(ctx context.Context, resource *v1alpha1.StackResource) {
	msg := resource.Status.LastFailureDetails[0].LastTerminationMessage
	r.Status.SetCondition(resource, v1alpha1.StackResourceWorkloadConverged, false, "PortNotListening", msg)
	r.Status.ReportFailed(ctx, resource, "PortNotListening", msg)
}
```

Update its two callers to pass `ctx`: `deployment.go:149` and `statefulset.go:98`.

- [ ] **Step 5: Compile, vet, and run the full unit suites**

Run: `go build ./... && go vet ./... && go test ./internal/... -count=1`
Expected: compile clean; **test failures are expected** in `stackresource` and `workload` suites where old assertions check summary conditions written mid-chain (e.g. `svc_reconciler_test.go` asserting `Available=True` after the svc reconciler alone, `availability_test.go`, `image_build_test.go` asserting `Phase=Failed` right after the imageBuild sub-reconciler). Triage each failure:
  - Assertion checked a **domain condition** (`BuildReady`, `WorkloadAvailable`, `WorkloadConverged`, `IngressReady`) → must still pass unchanged; if it fails, the refactor broke domain writes — fix the code, not the test.
  - Assertion checked **summary status mid-chain** → rewrite the test to run `deriveSummaryStatus(resource, collector)` after the sub-reconciler under test, using the collector attached via `controller.ContextWithVerdicts` in the test's ctx, then assert the derived result.

Expected after triage: PASS.

---

### Task 5: Chain-level test — pin the overwrite class shut

**Files:**
- Modify: `internal/controller/stackresource/status_derive_test.go` (append)

**Interfaces:**
- Consumes: `StackResourceReconciler.reconcile` chain semantics, `deriveSummaryStatus`, `defaultStatusReporter`, `controller.ContextWithVerdicts`.

This test reproduces the reported scenario at the chain level: an earlier sub-reconciler files a Failed verdict, a later one (svc-shaped) runs its full happy path afterwards — then asserts the **post-derivation** status. Before this refactor, this scenario ended `Available=True/Stalled=False/Phase=Degraded` with the verdict erased.

- [ ] **Step 1: Write the test**

Append to `status_derive_test.go`:

```go
// stubSubReconciler lets the test compose an arbitrary chain.
type stubSubReconciler struct {
	fn func(ctx context.Context, r *v1alpha1.StackResource) (subReconcilerResult, error)
}

func (s stubSubReconciler) reconcile(ctx context.Context, r *v1alpha1.StackResource) (subReconcilerResult, error) {
	return s.fn(ctx, r)
}

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
```

Add the needed imports (`context`) to the test file if missing. `resultContinue`/`resultNil` are package vars (`stackresource_controller.go:33-40`).

- [ ] **Step 2: Run it**

Run: `go test ./internal/controller/stackresource/ -run TestFailedVerdictSurvivesFullPass -v`
Expected: PASS

---

### Task 6: Integration verification and expectation drift

**Files:**
- Possibly modify: `test/integration/port_readiness_test.go`, `test/integration/stack_lifecycle_test.go`, `test/integration/stack_convergence_test.go`, `test/integration/helpers/stack_helpers.go` — wherever assertions name summary-condition reasons/messages that changed (`ServingButStalled`, the Degraded message, Pending reasons now uniform with verdict reasons, workload-written `Converged` now `WorkloadConverged`).

- [ ] **Step 1: Grep the integration suite for summary-status expectations**

Run: `grep -rn "Stalled\|Available\|Phase\|Converged\|PortNotListening\|StackResourceAvailable" test/integration/ --include="*.go"`
Review each hit against the Derivation Matrix; update expected reasons/messages where they changed. Summary condition **types** and polarities must not need changes — if a test expects a different polarity than the matrix produces, stop and re-check the derivation, not the test.

- [ ] **Step 2: Run the focused integration suites** (requires Docker; ~10 min each; results land in `test/integration/last-run.log`)

Run: `FOCUS="port" make test-integration`
Expected: PASS — this suite exercises the terminal port-failure path end-to-end.

Run: `FOCUS="Stack lifecycle" make test-integration`
Expected: PASS.

- [ ] **Step 3: Full unit sweep once more**

Run: `go build ./... && go vet ./... && go test ./internal/... ./pkg/... -count=1`
Expected: PASS.

---

### Task 7: Close the loop in the problem statement

**Files:**
- Modify: `docs/port-check-status-clobber.md` (Status section)

- [ ] **Step 1:** Change the Status line to: `Solution B implemented — summary status (Available/Stalled/Converged/Phase) derived once per pass in deriveSummaryStatus (internal/controller/stackresource/status_derive.go); sub-reconcilers write domain conditions (compiler-enforced via StackResourceDomainCondition, incl. new WorkloadConverged) and verdicts only. P5 (hub release semantics) remains open on the hub side: serving-with-terminal-failure is distinguishable as Stack Phase=Failed + Available=True.`

---

## Self-Review Notes

- **Spec coverage:** P1 (writer fight) → Tasks 1-4 remove the ability, Task 2's type split makes it a compile error; P2 (Stalled never reaches Stack) → Failed verdicts always derive `Stalled=True` (matrix rows 1-2, tested); P3 (terminal failure reported through the retriable path) → Task 4 Step 4 rewrites `reportPortNotListening` with `ReportFailed`; P4 (diagnosis demoted) → matrix rows 1-2 put the verdict reason on `Stalled`/`Converged` and a truthful `ServingButStalled` on `Available`; the false "previous revision" message is gone (matrix row 5). P5 is hub-side, documented in Task 7.
- **Converged classification:** summary at both levels — the Stack aggregate derives it from children; StackResource derives it from the new `WorkloadConverged` domain fact + verdicts. Single writer at every level; summary `Converged` is re-stamped each pass so it is never stale.
- **Deliberate exclusions:** `ReportReady` deleted rather than kept as a no-op — a method that does nothing invites someone to "fix" it. The Degraded message no longer distinguishes first-deploy vs prior-convergence (`LastConverged`); acceptable — the stack-level aggregate already makes that distinction.
- **Type consistency check:** `StatusReporter.SetCondition` takes `StackResourceDomainCondition` (Tasks 2, 4, 5); `setSummaryCondition` takes `StackResourceStatusCondition` and lives only in `status_derive.go` (Task 3); collector methods and accessors are `ReportNotReady/ReportFailed` and `NotReady()/Failed()` (Tasks 1, 3, 4, 5); `deriveSummaryStatus(resource, *controller.VerdictCollector)` consistent across Tasks 3-5; `domainConditionTrue` replaces both `workloadServing` and `isResourceConverged`.
