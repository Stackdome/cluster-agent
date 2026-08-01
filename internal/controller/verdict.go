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
