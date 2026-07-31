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
func (v *Verifier) Enqueue(key Key, podIP string, ports []int32) bool {
	v.mu.Lock()
	_, done := v.results[key]
	_, inFlight := v.pending[key]
	if done || inFlight {
		v.mu.Unlock()
		return false
	}
	v.pending[key] = struct{}{}
	v.mu.Unlock()

	select {
	case v.queue <- job{key: key, podIP: podIP, ports: ports}:
		return true
	default:
		// The queue is full, so this job will never run and its pending marker
		// must be cleared.
		v.mu.Lock()
		delete(v.pending, key)
		v.mu.Unlock()
		return false
	}
}

// Pending reports whether a dial for key is currently in flight — scheduled
// but not yet answered.
func (v *Verifier) Pending(key Key) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, inFlight := v.pending[key]
	return inFlight
}

// Forget drops any stored result for key, forcing the next Enqueue to redial.
// It reports whether a result was actually dropped, so a caller can tell a real
// discard from a no-op on a key that was never answered.
func (v *Verifier) Forget(key Key) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.results[key]
	delete(v.results, key)
	return ok
}
