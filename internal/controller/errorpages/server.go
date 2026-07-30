package errorpages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// DefaultAddr is the address the error-page server listens on inside the agent
// pod. It is distinct from the metrics and health probe addresses.
const DefaultAddr = ":8082"

// Handler serves the embedded error pages. Traefik's errors middleware requests
// /<status>; the catch-all route sends everything else here for a 404.
func Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		status, page := pageFor(strings.TrimPrefix(req.URL.Path, "/"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// These pages must never be cached: the same URL serves real content
		// again as soon as the workload recovers.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(page))
	})

	return mux
}

// pageFor resolves a request path to a status code and page body. An
// unrecognised path is a catch-all 404; a status with no page of its own falls
// back to the 500 page while keeping its real status code.
func pageFor(path string) (int, string) {
	code, err := strconv.Atoi(path)
	if err != nil || code < 100 || code > 599 {
		return http.StatusNotFound, Assets["404.html"]
	}
	if page, ok := Assets[fmt.Sprintf("%d.html", code)]; ok {
		return code, page
	}
	return code, Assets["500.html"]
}

// Runnable wraps the handler in a manager.Runnable so its lifecycle follows the
// manager's, shutting down cleanly when the agent stops.
func Runnable(addr string) manager.Runnable {
	return errorPageServer{addr: addr}
}

// errorPageServer serves the pages for the lifetime of the manager.
type errorPageServer struct {
	addr string
}

// NeedLeaderElection is false: the error-page Service selects every agent pod
// and readiness is probed on the health port, so a non-leader replica is Ready
// and lands in the Service's endpoints. Were this gated on the leader lease,
// those replicas would refuse connections on the error-page port.
func (errorPageServer) NeedLeaderElection() bool { return false }

func (s errorPageServer) Start(ctx context.Context) error {
	server := &http.Server{
		Addr:              s.addr,
		Handler:           Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
