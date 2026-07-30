package portcheck

import (
	"context"
	"sync"
	"time"
)

// DefaultWorkers bounds concurrent checks across every resource, not per
// resource, so dialing can never starve the process of file descriptors.
const DefaultWorkers = 8

// DefaultDialTimeout is the per-port TCP connect timeout.
const DefaultDialTimeout = 2 * time.Second

// queueDepth is deliberately generous: Enqueue drops rather than blocks when
// full, because a reconcile thread must never wait on this package.
const queueDepth = 512

// Key identifies one verification. Revision scopes a result to a deployment
// revision so a rollout invalidates the previous answer. It is deliberately
// the revision and not a pod-template-hash: callers hold the revision already,
// so a cache hit costs no API calls.
type Key struct {
	Namespace string
	Name      string
	Revision  string
}

type job struct {
	key        Key
	podIP      string
	ports      []int32
	generation int
}

// Verifier runs port checks on a bounded worker pool and stores their results
// for reconcilers to read without blocking.
type Verifier struct {
	workers int
	timeout time.Duration
	queue   chan job

	mu          sync.RWMutex
	results     map[Key]Result
	pending     map[Key]struct{}
	generations map[Key]int
}

func NewVerifier(workers int, timeout time.Duration) *Verifier {
	if workers <= 0 {
		workers = DefaultWorkers
	}
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	return &Verifier{
		workers:     workers,
		timeout:     timeout,
		queue:       make(chan job, queueDepth),
		results:     make(map[Key]Result),
		pending:     make(map[Key]struct{}),
		generations: make(map[Key]int),
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
			// Only proceed with cleanup if this job's generation is still current.
			// Stale jobs (from before a Forget) must do nothing to avoid clearing
			// state that belongs to the newer in-flight job with bumped generation.
			if v.generations[j.key] == j.generation {
				// Current job: write result and clean up its pending marker
				v.results[j.key] = result
				delete(v.pending, j.key)
				// Do NOT delete generations[j.key] — it may be needed by subsequent
				// Forget/Enqueue cycles or to validate incoming stale jobs.
			}
			// Stale job: do nothing. Pending and generations belong to the newer job.
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
func (v *Verifier) Enqueue(key Key, podIP string, ports []int32) {
	v.mu.Lock()
	_, done := v.results[key]
	_, inFlight := v.pending[key]
	if done || inFlight {
		v.mu.Unlock()
		return
	}
	v.pending[key] = struct{}{}
	// Initialize generation on first enqueue if not present.
	if _, ok := v.generations[key]; !ok {
		v.generations[key] = 0
	}
	generation := v.generations[key]
	v.mu.Unlock()

	select {
	case v.queue <- job{key: key, podIP: podIP, ports: ports, generation: generation}:
	default:
		v.mu.Lock()
		delete(v.pending, key)
		v.mu.Unlock()
	}
}

// Forget drops any stored result for key, forcing the next Enqueue to redial.
// It bumps the generation to invalidate any in-flight jobs from before the Forget.
// Note: generations[key] entries are never deleted. Bounded growth: entries exist only
// for keys that have been Forgotten. Cardinality is bounded by the number of distinct
// Key values ever enqueued, same order as the results map before Forget.
func (v *Verifier) Forget(key Key) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.results, key)
	delete(v.pending, key)
	v.generations[key]++
}
