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
		status, body := pageFor(strings.TrimPrefix(req.URL.Path, "/"))
		contentType := "text/html; charset=utf-8"
		if !wantsHTML(req) {
			contentType, body = "application/json; charset=utf-8", errorJSON(status)
		}
		w.Header().Set("Content-Type", contentType)
		// These pages must never be cached: the same URL serves real content
		// again as soon as the workload recovers.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})

	return mux
}

// wantsHTML reports whether the caller is a browser navigation. Traefik's errors
// middleware copies the original request's headers onto the request it makes
// here, so Accept still describes the real client. The match is on an explicit
// text/html: axios sends "application/json, text/plain, */*", so matching */*
// too would hand every API client an HTML body it cannot parse.
func wantsHTML(req *http.Request) bool {
	return strings.Contains(req.Header.Get("Accept"), "text/html")
}

// errorJSON renders the failure as the API error envelope the dashboard already
// parses, so a 5xx Traefik swapped out reaches the UI in the shape a real API
// error would. StatusText is ASCII from a fixed table, so %q is enough quoting.
func errorJSON(status int) string {
	return fmt.Sprintf(`{"type":"Error","reason":%q}`, http.StatusText(status))
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
