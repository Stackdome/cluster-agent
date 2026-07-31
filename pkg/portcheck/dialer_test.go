package portcheck

import (
	"context"
	"net"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// listenOnFreePort opens a real listener and returns its port plus a closer,
// so specs can distinguish a live port from a dead one without mocking.
func listenOnFreePort() (int32, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	return int32(ln.Addr().(*net.TCPAddr).Port), func() { _ = ln.Close() }
}

var _ = Describe("Dial", func() {
	var ctx context.Context

	BeforeEach(func() { ctx = context.Background() })

	It("reports a listening port as open", func() {
		port, closeFn := listenOnFreePort()
		DeferCleanup(closeFn)

		Expect(Dial(ctx, "127.0.0.1", []int32{port}, time.Second).AllOpen()).To(BeTrue())
	})

	It("reports a port with no listener as closed", func() {
		port, closeFn := listenOnFreePort()
		closeFn()

		result := Dial(ctx, "127.0.0.1", []int32{port}, time.Second)

		Expect(result.AllOpen()).To(BeFalse())
		Expect(result.ClosedPorts()).To(Equal([]int32{port}))
	})

	It("identifies exactly which port is dead when others are alive", func() {
		open, closeOpen := listenOnFreePort()
		DeferCleanup(closeOpen)
		dead, closeDead := listenOnFreePort()
		closeDead()

		result := Dial(ctx, "127.0.0.1", []int32{open, dead}, time.Second)

		Expect(result.AllOpen()).To(BeFalse())
		Expect(result.ClosedPorts()).To(Equal([]int32{dead}))
	})

	It("treats a resource with no declared ports as having nothing to fail", func() {
		Expect(Dial(ctx, "127.0.0.1", nil, time.Second).AllOpen()).To(BeTrue())
	})
})

var _ = Describe("Result.Message", func() {
	It("names the dead port and hints at the localhost mistake", func() {
		result := Result{Ports: []PortResult{{Port: 9090, Open: true}, {Port: 80, Open: false}}}

		msg := result.Message()

		Expect(msg).To(ContainSubstring("80"))
		Expect(msg).To(ContainSubstring("0.0.0.0"))
	})

	It("says so plainly when everything is listening", func() {
		result := Result{Ports: []PortResult{{Port: 80, Open: true}}}

		Expect(result.Message()).To(ContainSubstring("all declared ports are listening"))
	})
})
