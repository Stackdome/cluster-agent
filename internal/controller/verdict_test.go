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
