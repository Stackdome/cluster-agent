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
		v = newVerifier(2, time.Second)
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
		v = newVerifier(1, time.Second)
		v.Start(ctx)
		done := make(chan struct{})

		go func() {
			defer close(done)
			for i := 0; i < 1000; i++ {
				v.Enqueue(Key{Namespace: "ns", Name: "app", Revision: fmt.Sprint(i)}, "127.0.0.1", []int32{1})
			}
		}()

		Eventually(done).Within(3*time.Second).Should(BeClosed(),
			"Enqueue must drop rather than block; a reconcile thread cannot wait on this")
	})

	It("reports admission truthfully, including when the queue is full", func() {
		// Workers deliberately not started: every admitted job sits in the
		// queue, so exactly queueDepth admissions fit.
		for i := 0; i < queueDepth; i++ {
			k := Key{Namespace: "ns", Name: "app", Revision: fmt.Sprint(i)}
			Expect(v.Enqueue(k, "127.0.0.1", []int32{1})).To(BeTrue())
		}

		// Queue full: refused.
		Expect(v.Enqueue(Key{Namespace: "ns", Name: "app", Revision: "overflow"}, "127.0.0.1", []int32{1})).To(BeFalse())

		// A key already in flight is refused regardless of queue space.
		Expect(v.Enqueue(Key{Namespace: "ns", Name: "app", Revision: "0"}, "127.0.0.1", []int32{1})).To(BeFalse())
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

	It("refuses a second Enqueue while a dial for the same key is in flight", func() {
		// Single-flight: the pending marker set by the first Enqueue is held for
		// the whole dial, so a second Enqueue for the same key schedules nothing
		// — even though it names a port that would answer instantly.
		//
		// Two workers, so the refusal is what keeps the second check from
		// running rather than the queue simply being busy.
		v = newVerifier(2, 3*time.Second)
		v.Start(ctx)

		// Job A dials TEST-NET-1, so its Dial hangs for the full 3s timeout.
		v.Enqueue(key, testNetIP, []int32{1})
		time.Sleep(100 * time.Millisecond)

		// Job A must still be mid-Dial with nothing stored.
		_, ok := v.Get(key)
		Expect(ok).To(BeFalse(), testNetAssumption)

		// A second Enqueue on a live listener: refused, so no result appears
		// while job A holds the key.
		port, closeFn := listenOnFreePort()
		DeferCleanup(closeFn)
		v.Enqueue(key, "127.0.0.1", []int32{port})

		Consistently(func() bool { _, ok := v.Get(key); return ok }).
			Within(500*time.Millisecond).ProbeEvery(20*time.Millisecond).
			Should(BeFalse(), testNetAssumption)

		// Job A eventually lands its own (closed) answer, releasing the key.
		Eventually(func() bool {
			result, ok := v.Get(key)
			return ok && !result.AllOpen()
		}).Within(5 * time.Second).ProbeEvery(50 * time.Millisecond).Should(BeTrue())

		// With the dial no longer in flight, Forget + Enqueue is admitted and
		// the live result lands.
		v.Forget(key)
		v.Enqueue(key, "127.0.0.1", []int32{port})

		Eventually(func() bool {
			result, ok := v.Get(key)
			return ok && result.AllOpen()
		}).Within(3 * time.Second).ProbeEvery(20 * time.Millisecond).Should(BeTrue())
	})

	It("lets an in-flight job's answer land after a Forget, and corrects it on the next cycle", func() {
		// The accepted cost of single-flight, asserted so it stays a chosen
		// tradeoff rather than a surprise: Forget clears only the stored result,
		// never the pending marker, so a Forget+Enqueue issued mid-dial is
		// refused and the in-flight (stale) answer still lands in the cache.
		// The caller corrects it on its next reconcile.
		v = newVerifier(2, 3*time.Second)
		v.Start(ctx)

		port, closeFn := listenOnFreePort()
		DeferCleanup(closeFn)

		// Job A hangs on TEST-NET-1 and will answer "closed".
		v.Enqueue(key, testNetIP, []int32{1})
		time.Sleep(100 * time.Millisecond)
		_, ok := v.Get(key)
		Expect(ok).To(BeFalse(), testNetAssumption)

		// Mid-dial Forget + Enqueue: refused, nothing scheduled.
		v.Forget(key)
		v.Enqueue(key, "127.0.0.1", []int32{port})

		Consistently(func() bool { _, ok := v.Get(key); return ok }).
			Within(500*time.Millisecond).ProbeEvery(20*time.Millisecond).
			Should(BeFalse(), testNetAssumption)

		// Job A's stale answer lands regardless of the Forget.
		Eventually(func() bool {
			result, ok := v.Get(key)
			return ok && !result.AllOpen()
		}).Within(5*time.Second).ProbeEvery(50*time.Millisecond).Should(BeTrue(),
			"the in-flight job's answer is expected to land; discarding it is not this design's contract")

		// The next cycle's Forget + Enqueue is admitted and corrects it.
		v.Forget(key)
		v.Enqueue(key, "127.0.0.1", []int32{port})

		Eventually(func() bool {
			result, ok := v.Get(key)
			return ok && result.AllOpen()
		}).Within(3 * time.Second).ProbeEvery(20 * time.Millisecond).Should(BeTrue())
	})
})
