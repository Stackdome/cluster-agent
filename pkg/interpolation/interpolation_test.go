package interpolation

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestInterpolation(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Interpolation Suite")
}

var _ = Describe("Interpolator", func() {
	var (
		ctx          *InterpolationContext
		interpolator *Interpolator
	)

	BeforeEach(func() {
		// Set up a test context with sample resources
		ctx = &InterpolationContext{
			Resources: map[string]ResourceContext{
				"webapp": {
					Name: "webapp",
					Status: ResourceStatus{
						InternalService: stringPtr("webapp.default.svc.cluster.local"),
						PublicIngresses: []IngressContext{
							{
								URL:        "https://webapp.example.com",
								TargetPort: 8080,
							},
						},
					},
				},
				"web-app": {
					Name: "web-app",
					Status: ResourceStatus{
						InternalService: stringPtr("web-app.default.svc.cluster.local"),
						PublicIngresses: []IngressContext{
							{
								URL:        "https://web-app.example.com",
								TargetPort: 8080,
							},
						},
					},
				},
				"api-service": {
					Name: "api-service",
					Status: ResourceStatus{
						InternalService: stringPtr("api-service.default.svc.cluster.local"),
						PublicIngresses: []IngressContext{
							{
								URL:        "https://api.example.com",
								TargetPort: 8000,
							},
							{
								URL:        "https://api-alt.example.com",
								TargetPort: 9000,
							},
						},
					},
				},
				"internal-only": {
					Name: "internal-only",
					Status: ResourceStatus{
						InternalService: stringPtr("internal-only.default.svc.cluster.local"),
						PublicIngresses: []IngressContext{},
					},
				},
				"external-only": {
					Name: "external-only",
					Status: ResourceStatus{
						InternalService: nil,
						PublicIngresses: []IngressContext{
							{
								URL:        "https://external.example.com",
								TargetPort: 80,
							},
						},
					},
				},
			},
			Runtime: map[string]map[string]interface{}{
				"app": {
					"version": "1.0.0",
				},
			},
		}
		interpolator = NewInterpolator(ctx)
	})

	Describe("URL function", func() {
		Context("when resource exists with a single ingress", func() {
			It("returns the URL without port specification", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_PUBLIC_URL "web-app" }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("https://web-app.example.com"))
			})

			It("returns the URL with matching port specification", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_PUBLIC_URL "web-app" 8080 }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("https://web-app.example.com"))
			})

			It("fails with non-matching port specification", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_PUBLIC_URL "web-app" 9090 }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no ingress found for resource 'web-app' with port 9090"))
				Expect(result).To(Equal(""))
			})
		})

		Context("when resource exists with multiple ingresses", func() {
			It("fails when no port is specified", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_PUBLIC_URL "api-service" }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("multiple public ingresses found for resource 'api-service', specify a port"))
				Expect(result).To(Equal(""))
			})

			It("returns the correct URL when matching port is specified", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_PUBLIC_URL "api-service" 9000 }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("https://api-alt.example.com"))
			})

			It("returns the first ingress URL when matching port is specified", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_PUBLIC_URL "api-service" 8000 }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("https://api.example.com"))
			})
		})

		Context("when resource does not exist", func() {
			It("returns an error", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_PUBLIC_URL "non-existent" }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("resource 'non-existent' not found"))
				Expect(result).To(Equal(""))
			})
		})

		Context("when resource has no public ingresses", func() {
			It("returns an error", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_PUBLIC_URL "internal-only" }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("resource 'internal-only' has no public ingresses"))
				Expect(result).To(Equal(""))
			})
		})
	})

	Describe("Internal function", func() {
		Context("when resource exists with internal service", func() {
			It("returns the internal service address", func() {
				result, err := interpolator.InterpolateString(`{{ STACKRESOURCE_INTERNAL_ENDPOINT "web-app" }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("web-app.default.svc.cluster.local"))
			})
		})

		Context("when resource does not exist", func() {
			It("returns an error", func() {
				result, err := interpolator.InterpolateString(`{{ STACKRESOURCE_INTERNAL_ENDPOINT "non-existent" }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("resource 'non-existent' not found"))
				Expect(result).To(Equal(""))
			})
		})

		Context("when resource has no internal service", func() {
			It("returns an error", func() {
				result, err := interpolator.InterpolateString(`{{ STACKRESOURCE_INTERNAL_ENDPOINT "external-only" }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("resource 'external-only' has no internal service"))
				Expect(result).To(Equal(""))
			})
		})
	})

	Describe("InterpolateString", func() {
		Context("with valid templates", func() {
			It("interpolates simple resource references", func() {
				template := `Web URL: {{ STACKDOME_PUBLIC_URL "web-app" }}, API URL: {{ STACKDOME_PUBLIC_URL "api-service" 8000 }}`
				result, err := interpolator.InterpolateString(template)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("Web URL: https://web-app.example.com, API URL: https://api.example.com"))
			})

			It("interpolates internal addresses", func() {
				template := `Internal address: {{ STACKRESOURCE_INTERNAL_ENDPOINT "web-app" }}`
				result, err := interpolator.InterpolateString(template)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("Internal address: web-app.default.svc.cluster.local"))
			})

			It("interpolates context variables", func() {
				template := `App version: {{ .Runtime.app.version }}`
				result, err := interpolator.InterpolateString(template)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("App version: 1.0.0"))
			})

			It("combines multiple interpolations", func() {
				template := `Service: {{ .Resources.webapp.Name }}
							 Public URL: {{ STACKDOME_PUBLIC_URL "webapp" }}
							 Internal: {{ STACKRESOURCE_INTERNAL_ENDPOINT "webapp" }}
							 Version: {{ .Runtime.app.version }}`

				expected := `Service: webapp
							 Public URL: https://webapp.example.com
							 Internal: webapp.default.svc.cluster.local
							 Version: 1.0.0`

				result, err := interpolator.InterpolateString(template)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(expected))
			})
		})

		Context("with syntax errors", func() {
			It("returns a template parse error", func() {
				template := `{{ STACKDOME_PUBLIC_URL "unclosed }`
				result, err := interpolator.InterpolateString(template)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("template parse error"))
				Expect(result).To(Equal(""))
			})
		})

		Context("with missing keys", func() {
			It("returns a template execution error", func() {
				template := `{{ .Runtime.missing.key }}`
				result, err := interpolator.InterpolateString(template)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("template execution error"))
				Expect(result).To(Equal(""))
			})
		})
	})
})

func stringPtr(s string) *string {
	return &s
}
