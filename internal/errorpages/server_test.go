package errorpages

import (
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var _ = Describe("Handler", func() {
	var handler http.Handler

	BeforeEach(func() { handler = Handler() })

	// The pages are what a browser gets, so the shared helper asks for HTML the
	// way one does. The content-negotiation specs below set Accept themselves.
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "text/html")
		handler.ServeHTTP(rec, req)
		return rec
	}

	It("serves the page matching the status Traefik asks for", func() {
		// Traefik's errors middleware requests /<status> via its query field.
		rec := get("/502")

		Expect(rec.Code).To(Equal(http.StatusBadGateway))
		Expect(rec.Body.String()).To(Equal(Assets["502.html"]))
		Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/html"))
	})

	It("serves each status it has a page for", func() {
		for path, status := range map[string]int{
			"/404": http.StatusNotFound,
			"/500": http.StatusInternalServerError,
			"/503": http.StatusServiceUnavailable,
		} {
			rec := get(path)

			Expect(rec.Code).To(Equal(status), "path %s", path)
			Expect(rec.Body.Len()).To(BeNumerically(">", 0), "path %s", path)
		}
	})

	It("falls back to the 500 page for a status it has no page for", func() {
		rec := get("/507")

		Expect(rec.Body.String()).To(Equal(Assets["500.html"]))
	})

	It("serves the 404 page for the catch-all route", func() {
		// The catch-all IngressRoute forwards unmatched hostnames at /, and
		// visitors may land on any path underneath one.
		Expect(get("/").Body.String()).To(Equal(Assets["404.html"]))
		Expect(get("/some/deep/path").Body.String()).To(Equal(Assets["404.html"]))
	})

	// The middleware wraps every router on both entrypoints, so the dashboard's
	// own /api/v1 calls reach these pages too. Traefik copies the caller's
	// headers onto the request it makes here, so Accept picks the body shape.
	DescribeTable("picks the body shape from the caller's Accept header",
		func(accept, wantType, wantBody string) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/500", nil)
			if accept != "" {
				req.Header.Set("Accept", accept)
			}
			Handler().ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(rec.Header().Get("Content-Type")).To(ContainSubstring(wantType))
			Expect(rec.Body.String()).To(Equal(wantBody))
		},
		Entry("a browser navigation gets the page",
			"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			"text/html", Assets["500.html"]),
		// The */* here is why the match is on an explicit text/html: treating
		// */* as "browser" would hand axios an HTML body it cannot parse.
		Entry("axios gets the API error envelope",
			"application/json, text/plain, */*",
			"application/json", `{"type":"Error","reason":"Internal Server Error"}`),
		// Browsers always name text/html when navigating, so no Accept at all
		// is a plain HTTP client.
		Entry("a client sending no Accept gets the envelope",
			"", "application/json", `{"type":"Error","reason":"Internal Server Error"}`),
	)

	It("answers its own health check without a page body", func() {
		rec := get("/healthz")

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).NotTo(ContainSubstring("<html"))
	})
})

var _ = Describe("Runnable", func() {
	It("runs on every replica rather than only the leader", func() {
		// The error-page Service selects all agent pods and readiness is probed
		// on the health port, so a non-leader replica is Ready and receives
		// error-page traffic. If the server waited for the leader lease, those
		// replicas would refuse connections on the error-page port.
		runnable := Runnable(DefaultAddr)

		election, ok := runnable.(manager.LeaderElectionRunnable)
		Expect(ok).To(BeTrue(), "Runnable must declare its leader election behaviour")
		Expect(election.NeedLeaderElection()).To(BeFalse())
	})

	It("stops when the manager's context is cancelled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		// Port 0 lets the kernel pick a free port, so the spec never collides
		// with anything already listening.
		stopped := make(chan error, 1)
		go func() { stopped <- Runnable("127.0.0.1:0").Start(ctx) }()

		cancel()
		Eventually(stopped).Should(Receive(BeNil()))
	})
})
