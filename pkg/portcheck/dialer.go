// Package portcheck verifies that a workload is actually listening on the
// ports it declared. Kubernetes treats containerPort as informational, so
// without this a container listening on the wrong port is published to Service
// endpoints and returns 502 while every status signal reads healthy.
//
// Only ports the user declared are ever dialed. This package does not scan.
package portcheck

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"
)

// PortResult is the outcome of dialing a single declared port.
type PortResult struct {
	Port int32
	Open bool
}

// Result is the outcome of dialing every declared port of one workload.
type Result struct {
	Ports     []PortResult
	CheckedAt time.Time
}

// AllOpen reports whether every declared port accepted a connection. A
// workload with no declared ports has nothing to verify and is considered open.
func (r Result) AllOpen() bool {
	for _, p := range r.Ports {
		if !p.Open {
			return false
		}
	}
	return true
}

// ClosedPorts returns the declared ports that refused a connection, ascending.
func (r Result) ClosedPorts() []int32 {
	var closed []int32
	for _, p := range r.Ports {
		if !p.Open {
			closed = append(closed, p.Port)
		}
	}
	slices.Sort(closed)
	return closed
}

// Message renders an operator-facing explanation naming the dead ports. The
// localhost hint is included because binding to loopback is the second most
// common form of this mistake.
func (r Result) Message() string {
	closed := r.ClosedPorts()
	if len(closed) == 0 {
		return "all declared ports are listening"
	}
	verified := make([]string, 0, len(r.Ports))
	for _, p := range r.Ports {
		state := "refused"
		if p.Open {
			state = "open"
		}
		verified = append(verified, fmt.Sprintf("%d %s", p.Port, state))
	}
	names := make([]string, 0, len(closed))
	for _, p := range closed {
		names = append(names, fmt.Sprint(p))
	}
	plural := "port"
	if len(closed) > 1 {
		plural = "ports"
	}
	return fmt.Sprintf(
		"readiness check failed: nothing listening on %s %s (ports verified: [%s]). "+
			"If your app listens on a different port, update the port in your stack definition. "+
			"If it listens on 127.0.0.1, change it to 0.0.0.0.",
		plural, strings.Join(names, ", "), strings.Join(verified, ", "))
}

// Dial attempts a TCP connection to each port on host, concurrently. It never
// returns an error: an unreachable port is a result, not a failure of the check.
func Dial(ctx context.Context, host string, ports []int32, timeout time.Duration) Result {
	results := make([]PortResult, len(ports))
	var wg sync.WaitGroup
	dialer := net.Dialer{Timeout: timeout}

	for i, port := range ports {
		wg.Add(1)
		go func(i int, port int32) {
			defer wg.Done()
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
			if err == nil {
				_ = conn.Close()
			}
			results[i] = PortResult{Port: port, Open: err == nil}
		}(i, port)
	}
	wg.Wait()

	return Result{Ports: results, CheckedAt: time.Now().UTC()}
}
