package portcheck

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// testNetIP is RFC 5737 TEST-NET-1, reserved for documentation and never
// routed on the public internet. Dialing it normally hangs until the dial
// timeout, which is how the specs below hold a job in flight for a known
// duration without mocking the dialer.
const testNetIP = "192.0.2.1"

// testNetAssumption annotates every assertion that depends on that hang. On a
// network whose gateway answers TEST-NET-1 with ICMP unreachable the dial
// fails in milliseconds instead, and the spec can no longer stage the ordering
// it exists to test. Failing with this message names the environment as the
// suspect rather than leaving a bare unexplained boolean.
const testNetAssumption = "this spec needs a dial to " + testNetIP + " (RFC 5737 TEST-NET-1) to hang " +
	"until the dial timeout; if this network answers it with ICMP unreachable the dial returns " +
	"immediately and the interleaving under test cannot be staged"

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

		// Job A dials TEST-NET-1, so its Dial hangs for the full 3s timeout and
		// the job stays in flight across the Forget below.
		v.Enqueue(key, testNetIP, []int32{1})

		// Give job A a moment to be dequeued and start its Dial.
		time.Sleep(100 * time.Millisecond)

		// Job A must still be mid-Dial with nothing stored.
		_, ok := v.Get(key)
		Expect(ok).To(BeFalse(), testNetAssumption)

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

	It("does not let a stale job's completion clear the in-flight fresh job's state", func() {
		// The dangerous interleaving is the mirror of the spec above: the stale
		// job finishes FIRST, while the fresh job is still dialing. If the stale
		// job clears the shared pending marker and generation on its way out, it
		// hands the key to a third Enqueue and the fresh job's own result is then
		// discarded as stale.
		//
		// A single worker makes the ordering exact rather than probabilistic:
		// the queue is FIFO, so one long-running blocker job lets the whole queue
		// be staged before any of it starts dialing.
		const bPort = int32(5555)

		v = NewVerifier(1, time.Second)
		v.Start(ctx)

		aPort, closeA := listenOnFreePort()
		closeA() // nothing listening, so job A's dial fails in microseconds
		cPort, closeC := listenOnFreePort()
		closeC()

		blocker := Key{Namespace: "ns", Name: "blocker", Revision: "h1"}
		v.Enqueue(blocker, testNetIP, []int32{1})

		// Staged behind the blocker, in queue order: job A at generation 0, then
		// the Forget that invalidates it, then job B at generation 1.
		v.Enqueue(key, "127.0.0.1", []int32{aPort})
		v.Forget(key)
		v.Enqueue(key, testNetIP, []int32{bPort})

		// The blocker's result appearing means the worker is free and job A is
		// next off the queue.
		Eventually(func() bool { _, ok := v.Get(blocker); return ok }).
			Within(3 * time.Second).ProbeEvery(10 * time.Millisecond).
			Should(BeTrue(), testNetAssumption)

		// Job A now completes within microseconds and, being stale, must store
		// nothing. Job B takes the worker next and holds it for the full dial
		// timeout, so the key stays unanswered across this whole window.
		Consistently(func() bool { _, ok := v.Get(key); return ok }).
			Within(300 * time.Millisecond).ProbeEvery(20 * time.Millisecond).
			Should(BeFalse(), testNetAssumption)

		// Job C is admitted only if stale job A wrongly cleared the pending
		// marker that belongs to job B. Being admitted also resets the
		// generation, which is what makes B's own write look stale.
		v.Enqueue(key, "127.0.0.1", []int32{cPort})

		Eventually(func() []PortResult {
			result, ok := v.Get(key)
			if !ok {
				return nil
			}
			return result.Ports
		}).Within(3 * time.Second).ProbeEvery(20 * time.Millisecond).
			Should(Equal([]PortResult{{Port: bPort, Open: false}}),
				"the stored result must be job B's (port %d, refused); port %d would mean job C "+
					"was admitted over B's pending marker and B's result was discarded as stale",
				bPort, cPort)
	})
})
