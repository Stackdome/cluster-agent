# Port-check condemnation is overwritten by the Service reconciler

## Status

Solution B implemented — summary status (Available/Stalled/Converged/Phase) derived once per pass in deriveSummaryStatus (internal/controller/stackresource/status_derive.go); sub-reconcilers write domain conditions (compiler-enforced via StackResourceDomainCondition, incl. new WorkloadConverged) and verdicts only. P5 (hub release semantics) remains open on the hub side: serving-with-terminal-failure is distinguishable as Stack Phase=Failed + Available=True.

## Background

The port verifier condemns a workload that is serving (kubelet probe passed on
the primary port) but has a declared secondary port that stayed closed past the
3-minute grace window (`workload/readiness.go`, `DefaultPortCheckGrace`). The
verdict is terminal per revision: the cached closed verdict is kept and never
re-dialled until a new rollout (`verifyServingPorts`, readiness.go:114-119).

## Current behaviour, traced

One reconcile pass over a condemned resource. Sub-reconciler order is fixed in
`NewStackResourceReconciler` (stackresource_controller.go:324-342):
registryAuth → imageBuild → workload → svc.

**1. Workload reconciler** (deployment.go serving branch, ~:126-155):

- Sets `WorkloadAvailable=True (DeploymentServing)` — the deployment IS serving.
- `verifyServingPorts` returns `failed=true` → `reportPortNotListening`
  (readiness.go:233):
  - `WorkloadAvailable=False (PortNotListening)` — overwrites the True written
    moments earlier in the same pass.
  - `Converged=False (PortNotListening)`
  - `ReportNotReady(...)` → `reportStackResourceNotReady`
    (stackresource_controller.go:114): top-level `Available=False`,
    `Stalled=False`, `Phase=Pending`.
- Returns `ResultContinue` — deliberate: “The chain keeps running; conditions
  carry the verdict” (readiness.go:230). The Service/Ingress of a serving
  workload must keep being managed.

**2. svc reconciler** (svc_reconciler.go:33): every non-error exit calls
`reportStackResourceReady` unconditionally (:37, :49, :62), which writes:

- `Available=True` — reason `StackResourceAvailable`, message “available on
  previous revision; current rollout not converged” (because Converged is
  False). On a first deploy there is no previous revision; the message is
  false.
- `Stalled=False (StackResourceAvailable)`
- `Phase=Degraded` (not-converged branch of `reportStackResourceReady`).

**3. Final status written to the API server** (single status update at end of
`Reconcile`):

| Field | Value | Verdict content |
|---|---|---|
| Phase | `Degraded` | terminal failure shown as transient |
| Available | `True (StackResourceAvailable)` | port verdict text gone; message can be false |
| WorkloadAvailable | `False (PortNotListening)` | survives |
| Converged | `False (PortNotListening)` | survives |
| Stalled | `False` | terminal-ness erased |
| LastFailureDetails | PortNotListening detail | survives |

## The problems

**P1 — Last-writer-wins on the summary conditions.** Two sub-reconcilers write
`Available`/`Stalled`/`Phase` with opposing opinions in one pass. The final
value depends purely on chain order. Nothing — no code structure, no test —
pins that order or the outcome. Reordering the chain or adding an early return
flips the verdict silently.

**P2 — The terminal signal never reaches the Stack.** Stack aggregation reads
`Stalled` to distinguish retriable from terminal children
(stack/aggregate.go:239). The condemned resource ends the pass with
`Stalled=False`, `Available=True`, `Converged=False` — indistinguishable from a
routine in-flight rollout. On a first deploy (`LastConverged == nil`,
`Degraded` requires a prior convergence) the Stack shows
`Progressing/RolloutInProgress` **forever**. A permanently-failed revision is
presented as making progress, which is the exact lie the Stalled condition
exists to prevent.

**P3 — The condemnation itself uses the retriable path.** Even before the svc
clobber, `reportPortNotListening` calls `ReportNotReady` — Phase=Pending,
Stalled=False — for a verdict its own comment calls “Terminal per revision.”
The terminal/retriable mismatch exists inside the port-check code, not just in
the overwrite.

**P4 — The diagnosis is demoted.** `Available` ends the pass carrying generic
success text. The `PortNotListening` reason survives only on
`WorkloadAvailable`/`Converged`/`LastFailureDetails`. Anyone (human or hub)
checking the top-level `Available` condition — the natural first stop — sees a
healthy resource with a misleading message.

**P5 — Release-level semantics in the hub.** The Stack is tied to a release;
the hub maps a stalled, non-converged Stack to a **failed release**. For a
serving-condemned resource that is half-true: against the declared contract
(image + ports) the rollout never succeeded and never will on this revision —
but the workload is serving traffic on it. "Failed" implies nothing is
running; "Succeeded" would ship a dead declared port silently. The release
outcome is binary where reality is three-valued (serving / not converged /
terminal). Note the agent already exports enough to distinguish the cases once
the fix lands — no new API needed:

```
dead workload:      Stack Phase=Failed, Available=False
serving-condemned:  Stack Phase=Failed, Available=True
```

The mapping belongs in the hub: `Stalled && !Available` → Failed (unchanged);
`Stalled && Available` → failed-but-serving, expressed either as a distinct
release state (e.g. Degraded) or as Failed with a reason/message naming the
dead port. If the hub takes automated action on Failed (rollback, retry,
paging), a serving-condemned release must be exempted from that reflex —
rolling back a serving app over a misdeclared metrics port is worse than the
condition it reacts to; that risk is the strongest argument for a distinct
state over a message.

Note what is *not* wrong: the final `Available=True` **value** is actually the
desired end state — the workload is serving and the serving rollup should say
so. It is correct by collision, with the wrong reason/message attached, while
dragging Phase and Stalled along with it.

## Desired end state for a condemned resource

```
Phase              = Failed          (terminal wins, matches aggregate contract)
Available          = True            (serving — kubelet says so; reason should say "serving but condemned")
WorkloadAvailable  = False           (PortNotListening — which port, what verdict)
Converged          = False           (PortNotListening)
Stalled            = True            (PortNotListening — final for this revision)
```

Stack aggregate consequences (already correct once Stalled arrives): child
counts as stalled → Stack Phase=Failed, Ready=False (ResourceStalled);
Available rollup may stay True because the child serves. Clears automatically
on the next rollout: new revision restarts the port check, and the ready path
resets Stalled.

Deliberate consequence: `dependsOn` gating reads `Available`, so a
serving-but-stalled sibling no longer blocks its dependents (before the fix it
did) — a conscious decision, serving means serving.

## Solution options

**A. Guard the shared ready-reporter (small, recommended first step).**
`reportStackResourceReady` refuses to overwrite a same-generation terminal
verdict: if `Stalled=True` with `ObservedGeneration == resource.Generation`,
return without touching Available/Stalled/Phase (still safe to do its
bookkeeping like ObservedRevision). Pair with fixing P3:
`reportPortNotListening` stops calling `ReportNotReady` and instead writes the
desired end state above (Available=True with a “serving but condemned” reason,
Stalled=True, Phase=Failed). Two focused edits in the two functions that own
the bug; every caller of `reportStackResourceReady` inherits the guard.

**B. Single-writer status derivation (the structural fix).** No sub-reconciler
writes `Available`/`Stalled`/`Phase` directly. Each writes only its domain
conditions (BuildReady, WorkloadAvailable, IngressReady, Converged); a final
derivation step in `Reconcile` computes the summary trio from them — the same
pattern `stack/aggregate.go` already uses one level up. Eliminates the
writer-fight class entirely, at the cost of touching every report call site.
Right shape if summary-condition writers keep multiplying; oversized for this
bug alone.

**C. Stop the chain on condemnation.** Make the workload reconciler return
`ResultStop` after `reportPortNotListening`, like other terminal paths.
Rejected: the condemned workload is still serving; its Service and Ingress
must keep being reconciled. `ResultContinue` is correct — the bug is in what
gets overwritten, not in continuing.

**D. Make svcReconciler check conditions before reporting ready.** Same effect
as A but placed in the caller; every future sub-reconciler added after
workload would need the same check. The guard belongs in the shared function,
not copied into call sites.

## Test to pin it

One spec (envtest or integration) asserting the **post-pass** status of a
condemned resource — after a full reconcile including the svc reconciler, not
after the port-check step: `Stalled=True`, `Phase=Failed`, `Available=True`
with the condemned reason, `Converged=False (PortNotListening)`. This makes
the chain-order coincidence unrecreatable regardless of which solution lands.
