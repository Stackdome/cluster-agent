package controller

import "time"

// SubReconcilerResult signals how the sub-reconciler chain should proceed.
type SubReconcilerResult struct {
	ResultNil            bool
	ResultStop           bool
	ResultRequeue        bool
	ResultRequeueAfter   *time.Duration
	DeferredRequeueAfter *time.Duration
}

var (
	ResultNil      = SubReconcilerResult{ResultNil: true}
	ResultStop     = SubReconcilerResult{ResultStop: true}
	ResultRequeue  = SubReconcilerResult{ResultRequeue: true}
	ResultContinue = ResultNil
)

func ResultRequeueAfter(t time.Duration) SubReconcilerResult {
	return SubReconcilerResult{ResultRequeueAfter: &t}
}

func ResultDeferredRequeue(t time.Duration) SubReconcilerResult {
	return SubReconcilerResult{DeferredRequeueAfter: &t}
}
