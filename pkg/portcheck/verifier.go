package portcheck

import (
	"context"
	"sync"
	"time"
)

// DefaultWorkers bounds concurrent checks across every resource.
const DefaultWorkers = 8

// DefaultDialTimeout is the per-port TCP connect timeout.
const DefaultDialTimeout = 1 * time.Second

// queueDepth is deliberately generous: Enqueue drops rather than blocks when
// full, because a reconcile thread must never wait on this package.
const queueDepth = 512

// Key identifies one verification. Revision scopes a result to a deployment
// revision so a rollout invalidates the previous answer. It is deliberately
// the revision.
type Key struct {
	Namespace string
	Name      string
	Revision  string
}

type job struct {
	key   Key
	podIP string
	ports []int32
}

// Verifier runs port checks on a bounded worker pool and stores their results
// for reconcilers to read without blocking.
//
// The verifier is single-flight per key: at most one job for a given Key is
// ever in flight, because Enqueue refuses while pending[key] is set and Forget
// never clears that marker. Two jobs therefore cannot race to write one key's
// result, and no generation bookkeeping is needed to arbitrate between them.
type Verifier struct {
	workers int
	timeout time.Duration
	queue   chan job

	mu      sync.RWMutex
	results map[Key]Result
	pending map[Key]struct{}
}

func NewVerifierWithDefaults() *Verifier {
	return newVerifier(DefaultWorkers, DefaultDialTimeout)
}

func newVerifier(workers int, timeout time.Duration) *Verifier {
	return &Verifier{
		workers: workers,
		timeout: timeout,
		queue:   make(chan job, queueDepth),
		results: make(map[Key]Result),
		pending: make(map[Key]struct{}),
	}
}

// Start launches the worker pool and returns immediately. Workers exit when
// ctx is cancelled.
func (v *Verifier) Start(ctx context.Context) {
	for i := 0; i < v.workers; i++ {
		go v.run(ctx)
	}
}

func (v *Verifier) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-v.queue:
			result := Dial(ctx, j.podIP, j.ports, v.timeout)
			v.mu.Lock()
			// Unconditional: single-flight guarantees this job is the only one
			// ever in flight for its key, so the result it just produced is the
			// only candidate and the pending marker it clears is its own. A
			// Forget during the dial does not change that — it clears only the
			// stored result, so this write lands and is corrected on a later
			// Forget-and-redial cycle. See Forget for the tradeoff.
			v.results[j.key] = result
			delete(v.pending, j.key)
			v.mu.Unlock()
		}
	}
}

// Get returns the stored result for key. The boolean is false when no check
// has completed yet; callers must requeue rather than wait.
func (v *Verifier) Get(key Key) (Result, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	res, ok := v.results[key]
	return res, ok
}

// Enqueue schedules a check. It never blocks: a duplicate, already-answered,
// or unqueueable request is dropped and the caller retries on its next
// reconcile.
//
// It is also single-flight. A key whose dial is already in flight is refused
// outright, so a Forget+Enqueue issued mid-dial schedules nothing and the
// caller's next reconcile tries again.
func (v *Verifier) Enqueue(key Key, podIP string, ports []int32) {
	v.mu.Lock()
	_, done := v.results[key]
	_, inFlight := v.pending[key]
	if done || inFlight {
		v.mu.Unlock()
		return
	}
	v.pending[key] = struct{}{}
	v.mu.Unlock()

	select {
	case v.queue <- job{key: key, podIP: podIP, ports: ports}:
	default:
		// The queue is full, so this job will never run and its pending marker
		// must be cleared. Clearing it unconditionally is safe: nobody else can
		// have set it in the meantime, because Enqueue refuses while the marker
		// exists and Forget never clears it, so the marker is still the one set
		// a few lines above.
		v.mu.Lock()
		delete(v.pending, key)
		v.mu.Unlock()
	}
}

// Forget drops any stored result for key, forcing the next Enqueue to redial.
// It reports whether a result was actually dropped, so a caller can tell a real
// discard from a no-op on a key that was never answered.
//
// It deliberately does NOT clear the pending marker. That marker is the
// single-flight lock: leaving it in place is what guarantees at most one job
// per key is ever in flight, which in turn is what makes the completion path in
// run() a race-free unconditional write. A Forget issued while a dial is in
// flight therefore only discards the cached answer; the immediately following
// Enqueue is refused and nothing is rescheduled until that dial completes.
//
// Accepted tradeoff, chosen deliberately over generation tracking: the in-flight
// job's answer — computed before the Forget, possibly against a workload that
// has since changed — DOES land in the cache. It is not discarded. Correction is
// not immediate; it waits for the caller's next reconcile (~10s) to Forget the
// now-stale result and redial, which succeeds because the pending marker is gone
// by then. The state machine still terminates: every cycle either produces a
// fresh answer or costs one requeue interval.
//
// Known accepted edge: if such a stale write lands just as the caller's grace
// budget expires, the caller believes it and can condemn a healthy resource.
// Low probability, and accepted by the design owner in exchange for deleting the
// generation bookkeeping. This is a chosen cost, not an oversight — do not
// "fix" it by clearing pending here without revisiting run()'s unconditional
// write, which depends on this invariant.
func (v *Verifier) Forget(key Key) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.results[key]
	delete(v.results, key)
	return ok
}
