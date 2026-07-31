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

// Key identifies one verification. Results are stored per (Namespace, Name)
// with the revision alongside the answer: Get and Forget only match when the
// stored revision equals the requested one, and a new revision's dial
// overwrites the old entry, so a rollout evicts the previous answer instead of
// orphaning it.
type Key struct {
	Namespace string
	Name      string
	Revision  string
}

func (k Key) nameKey() nameKey { return nameKey{namespace: k.Namespace, name: k.Name} }

type nameKey struct {
	namespace string
	name      string
}

type storedResult struct {
	revision string
	result   Result
}

type job struct {
	key   Key
	podIP string
	ports []int32
}

// Verifier runs port checks on a bounded worker pool and stores their results
// for reconcilers to read without blocking.
//
// The verifier is single-flight per resource: at most one job for a given
// (namespace, name) is ever in flight, because Enqueue refuses while the
// pending marker is set and only the worker clears it. Two jobs therefore
// cannot race to write one resource's result.
type Verifier struct {
	workers int
	timeout time.Duration
	queue   chan job

	mu      sync.RWMutex
	results map[nameKey]storedResult
	pending map[nameKey]struct{}
}

func NewVerifierWithDefaults() *Verifier {
	return newVerifier(DefaultWorkers, DefaultDialTimeout)
}

func newVerifier(workers int, timeout time.Duration) *Verifier {
	return &Verifier{
		workers: workers,
		timeout: timeout,
		queue:   make(chan job, queueDepth),
		results: make(map[nameKey]storedResult),
		pending: make(map[nameKey]struct{}),
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
			v.results[j.key.nameKey()] = storedResult{revision: j.key.Revision, result: result}
			delete(v.pending, j.key.nameKey())
			v.mu.Unlock()
		}
	}
}

// Get returns the stored result for key. The boolean is false when no check
// for key's revision has completed yet; callers must requeue rather than wait.
func (v *Verifier) Get(key Key) (Result, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	stored, ok := v.results[key.nameKey()]
	if !ok || stored.revision != key.Revision {
		return Result{}, false
	}
	return stored.result, true
}

// Enqueue schedules a check. It never blocks: a duplicate, already-answered,
// or unqueueable request is dropped and the caller retries on its next
// reconcile. An entry from a previous revision does not refuse the job — its
// dial overwrites the stale answer.
//
// It is also single-flight. A resource whose dial is already in flight is
// refused outright, so a Forget+Enqueue issued mid-dial schedules nothing and
// the caller's next reconcile tries again.
func (v *Verifier) Enqueue(key Key, podIP string, ports []int32) bool {
	v.mu.Lock()
	stored, done := v.results[key.nameKey()]
	_, inFlight := v.pending[key.nameKey()]
	if (done && stored.revision == key.Revision) || inFlight {
		v.mu.Unlock()
		return false
	}
	v.pending[key.nameKey()] = struct{}{}
	v.mu.Unlock()

	select {
	case v.queue <- job{key: key, podIP: podIP, ports: ports}:
		return true
	default:
		// The queue is full, so this job will never run and its pending marker
		// must be cleared.
		v.mu.Lock()
		delete(v.pending, key.nameKey())
		v.mu.Unlock()
		return false
	}
}

// Pending reports whether a dial for key's resource is currently in flight —
// scheduled but not yet answered.
func (v *Verifier) Pending(key Key) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, inFlight := v.pending[key.nameKey()]
	return inFlight
}

// Forget drops the stored result for key's revision, forcing the next Enqueue
// to redial.
func (v *Verifier) Forget(key Key) {
	v.mu.Lock()
	defer v.mu.Unlock()
	stored, ok := v.results[key.nameKey()]
	if !ok || stored.revision != key.Revision {
		return
	}
	delete(v.results, key.nameKey())
}
