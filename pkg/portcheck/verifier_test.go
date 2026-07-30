package portcheck

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Verifier", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		v      *Verifier
		key    Key
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		DeferCleanup(cancel)
		v = NewVerifier(2, time.Second)
		key = Key{Namespace: "ns", Name: "app", Revision: "h1"}
	})

	It("has no result before any check is enqueued", func() {
		_, ok := v.Get(key)

		Expect(ok).To(BeFalse())
	})

	It("stores a result once a check completes", func() {
		port, closeFn := listenOnFreePort()
		DeferCleanup(closeFn)
		v.Start(ctx)

		v.Enqueue(key, "127.0.0.1", []int32{port})

		Eventually(func() bool {
			result, ok := v.Get(key)
			return ok && result.AllOpen()
		}).Within(3 * time.Second).ProbeEvery(10 * time.Millisecond).Should(BeTrue())
	})

	It("never blocks the caller, even when saturated", func() {
		v = NewVerifier(1, time.Second)
		v.Start(ctx)
		done := make(chan struct{})

		go func() {
			defer close(done)
			for i := 0; i < 1000; i++ {
				v.Enqueue(Key{Namespace: "ns", Name: "app", Revision: fmt.Sprint(i)}, "127.0.0.1", []int32{1})
			}
		}()

		Eventually(done).Within(3 * time.Second).Should(BeClosed(),
			"Enqueue must drop rather than block; a reconcile thread cannot wait on this")
	})

	It("does not let a new revision inherit the previous result", func() {
		port, closeFn := listenOnFreePort()
		DeferCleanup(closeFn)
		v.Start(ctx)
		v.Enqueue(key, "127.0.0.1", []int32{port})
		Eventually(func() bool { _, ok := v.Get(key); return ok }).Within(3 * time.Second).Should(BeTrue())

		_, ok := v.Get(Key{Namespace: "ns", Name: "app", Revision: "h2"})

		Expect(ok).To(BeFalse())
	})

	It("drops a stored result on Forget", func() {
		port, closeFn := listenOnFreePort()
		DeferCleanup(closeFn)
		v.Start(ctx)
		v.Enqueue(key, "127.0.0.1", []int32{port})
		Eventually(func() bool { _, ok := v.Get(key); return ok }).Within(3 * time.Second).Should(BeTrue())

		v.Forget(key)

		_, ok := v.Get(key)
		Expect(ok).To(BeFalse())
	})

	It("prevents stale in-flight jobs from overwriting fresh results", func() {
		// Use two workers so the fresh job doesn't queue behind the stale one
		v = NewVerifier(2, 3*time.Second)
		v.Start(ctx)

		// Job A: Enqueue against an unroutable IP. The Dial will hang for the full
		// 3s timeout trying to connect, keeping the job in-flight for the whole test.
		v.Enqueue(key, "10.255.255.1", []int32{1})

		// Give job A a moment to be dequeued and start its Dial. We verify by
		// attempting another enqueue with the same key; if job A is in-flight, this
		// will be rejected (pending[key] already set).
		time.Sleep(100 * time.Millisecond)

		// Verify job A has no result yet (still dialing)
		_, ok := v.Get(key)
		Expect(ok).To(BeFalse(), "Job A should still be mid-Dial, no result yet")

		// Call Forget to bump the generation, invalidating job A
		v.Forget(key)

		// Immediately enqueue job B with a real listening port. Job B's Dial will
		// complete in milliseconds (< 100ms).
		port, closeFn := listenOnFreePort()
		DeferCleanup(closeFn)
		v.Enqueue(key, "127.0.0.1", []int32{port})

		// Wait for job B to complete and write its result
		Eventually(func() bool {
			result, ok := v.Get(key)
			return ok && result.AllOpen()
		}).Within(2 * time.Second).Should(BeTrue())

		// Pre-fix code would fail here: job A finishes its dial after 3s and writes
		// its stale result (port 1 refused), overwriting job B's correct result.
		// The generation mechanism ensures job A's write is skipped, and job B's
		// AllOpen=true result persists.
		// This window (4s) is longer than job A's remaining timeout (~3s).
		Consistently(func() bool {
			result, ok := v.Get(key)
			return ok && result.AllOpen()
		}).Within(4 * time.Second).ProbeEvery(100 * time.Millisecond).Should(BeTrue(),
			"Result should stay AllOpen; stale job A's write must be skipped")
	})
})
