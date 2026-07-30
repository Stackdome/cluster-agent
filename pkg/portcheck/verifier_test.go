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
})
