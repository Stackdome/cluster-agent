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
								ExposedToPublic: true,
								URL:             "https://webapp.example.com",
								TargetPort:      8080,
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
								ExposedToPublic: true,
								URL:             "https://web-app.example.com",
								TargetPort:      8080,
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
								ExposedToPublic: true,
								URL:             "https://api.example.com",
								TargetPort:      8000,
							},
							{
								ExposedToPublic: true,
								URL:             "https://api-alt.example.com",
								TargetPort:      9000,
							},
						},
					},
				},
				"not-exposed": {
					Name: "not-exposed",
					Status: ResourceStatus{
						InternalService: stringPtr("not-exposed.default.svc.cluster.local"),
						PublicIngresses: []IngressContext{
							{
								ExposedToPublic: false,
								URL:             "https://not-exposed.example.com",
								TargetPort:      8080,
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
								ExposedToPublic: true,
								URL:             "https://external.example.com",
								TargetPort:      80,
							},
						},
					},
				},
			},
		}
		interpolator = NewInterpolator(ctx)
	})

	Describe("Resource-specific internal endpoints", func() {
		Context("when resource exists with internal service", func() {
			It("returns the internal service address for webapp", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_WEBAPP_INTERNAL }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("webapp.default.svc.cluster.local"))
			})

			It("returns the internal service address for resource with hyphen", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_WEB_APP_INTERNAL }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("web-app.default.svc.cluster.local"))
			})
		})

		Context("when resource does not exist", func() {
			It("returns an error for non-existent resource function", func() {
				_, err := interpolator.InterpolateString(`{{ STACKDOME_NONEXISTENT_INTERNAL }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Resource reference 'STACKDOME_NONEXISTENT_INTERNAL' is not available"))

				// Verify it's a TemplateError
				templateErr, ok := err.(*TemplateError)
				Expect(ok).To(BeTrue())
				Expect(templateErr.Original).NotTo(BeNil())
			})
		})

		Context("when resource has no internal service", func() {
			It("returns an error when internal service is nil", func() {
				_, err := interpolator.InterpolateString(`{{ STACKDOME_EXTERNAL_ONLY_INTERNAL }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("doesn't have an internal service"))

				// Verify it's a TemplateError
				templateErr, ok := err.(*TemplateError)
				Expect(ok).To(BeTrue())
				Expect(templateErr.Original).NotTo(BeNil())
			})
		})
	})

	Describe("Resource-specific public URLs", func() {
		Context("when resource exists with a single ingress", func() {
			It("returns the URL using default public function", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_WEBAPP_PUBLIC }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("https://webapp.example.com"))
			})

			It("returns the URL using port-specific function", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_WEBAPP_PUBLIC_8080 }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("https://webapp.example.com"))
			})

			It("returns an error for non-existent port function", func() {
				_, err := interpolator.InterpolateString(`{{ STACKDOME_WEBAPP_PUBLIC_9090 }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Resource reference 'STACKDOME_WEBAPP_PUBLIC_9090' is not available"))

				// Verify it's a TemplateError
				templateErr, ok := err.(*TemplateError)
				Expect(ok).To(BeTrue())
				Expect(templateErr.Original).NotTo(BeNil())
			})
		})

		Context("when resource exists with multiple ingresses", func() {
			It("has no default public function without port", func() {
				_, err := interpolator.InterpolateString(`{{ STACKDOME_API_SERVICE_PUBLIC }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Resource reference 'STACKDOME_API_SERVICE_PUBLIC' is not available"))

				// Verify it's a TemplateError
				templateErr, ok := err.(*TemplateError)
				Expect(ok).To(BeTrue())
				Expect(templateErr.Original).NotTo(BeNil())
			})

			It("returns the correct URL with port-specific functions", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_API_SERVICE_PUBLIC_9000 }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("https://api-alt.example.com"))
			})

			It("returns the URL for a different port", func() {
				result, err := interpolator.InterpolateString(`{{ STACKDOME_API_SERVICE_PUBLIC_8000 }}`)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("https://api.example.com"))
			})
		})

		Context("when resource is not exposed to public", func() {
			It("returns an error when attempting to access unexposed ingress", func() {
				_, err := interpolator.InterpolateString(`{{ STACKDOME_NOT_EXPOSED_PUBLIC }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("doesn't have a public URL"))

				// Verify it's a TemplateError
				templateErr, ok := err.(*TemplateError)
				Expect(ok).To(BeTrue())
				Expect(templateErr.Original).NotTo(BeNil())
			})
		})

		Context("when resource has no public ingresses", func() {
			It("has no public URL functions defined", func() {
				_, err := interpolator.InterpolateString(`{{ STACKDOME_INTERNAL_ONLY_PUBLIC }}`)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Resource reference 'STACKDOME_INTERNAL_ONLY_PUBLIC' is not available"))

				// Verify it's a TemplateError
				templateErr, ok := err.(*TemplateError)
				Expect(ok).To(BeTrue())
				Expect(templateErr.Original).NotTo(BeNil())
			})
		})
	})

	Describe("InterpolateString", func() {
		Context("with valid templates", func() {
			It("interpolates simple resource references", func() {
				template := `Web URL: {{ STACKDOME_WEB_APP_PUBLIC }}, API URL: {{ STACKDOME_API_SERVICE_PUBLIC_8000 }}`
				result, err := interpolator.InterpolateString(template)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("Web URL: https://web-app.example.com, API URL: https://api.example.com"))
			})

			It("interpolates internal addresses", func() {
				template := `Internal address: {{ STACKDOME_WEBAPP_INTERNAL }}`
				result, err := interpolator.InterpolateString(template)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("Internal address: webapp.default.svc.cluster.local"))
			})

			It("combines multiple interpolations", func() {
				template := `Service Internal: {{ STACKDOME_WEBAPP_INTERNAL }}
							 Public URL: {{ STACKDOME_WEBAPP_PUBLIC }}`

				expected := `Service Internal: webapp.default.svc.cluster.local
							 Public URL: https://webapp.example.com`

				result, err := interpolator.InterpolateString(template)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(expected))
			})

			It("handles strings with no interpolation", func() {
				template := `This is a plain string with no interpolation.`
				result, err := interpolator.InterpolateString(template)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("This is a plain string with no interpolation."))
			})

			It("handles empty strings", func() {
				template := ``
				result, err := interpolator.InterpolateString(template)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(""))
			})
		})

		Context("with syntax errors", func() {
			It("returns a template parse error for unclosed braces", func() {
				template := `{{ STACKDOME_WEBAPP_PUBLIC }`
				result, err := interpolator.InterpolateString(template)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Make sure all '{{' have matching '}}' pairs"))

				// Verify it's a TemplateError
				templateErr, ok := err.(*TemplateError)
				Expect(ok).To(BeTrue())
				Expect(templateErr.Original).NotTo(BeNil())
				Expect(result).To(Equal(""))
			})

			It("returns a template parse error for bad characters", func() {
				template := `{{ STACKDOME-WEBAPP-PUBLIC }}`
				result, err := interpolator.InterpolateString(template)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Contains characters that aren't allowed"))

				// Verify it's a TemplateError
				templateErr, ok := err.(*TemplateError)
				Expect(ok).To(BeTrue())
				Expect(templateErr.Original).NotTo(BeNil())

				Expect(result).To(Equal(""))
			})
		})

		Context("with mixed interpolation and plain text", func() {
			It("handles partial interpolation correctly", func() {
				template := `This is the service URL: {{ STACKDOME_WEBAPP_PUBLIC }} and this is plain text.`
				result, err := interpolator.InterpolateString(template)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("This is the service URL: https://webapp.example.com and this is plain text."))
			})

			It("handles partial interpolation with errors", func() {
				template := `This is the service URL: {{ STACKDOME_NONEXISTENT_PUBLIC }} and this is plain text.`
				result, err := interpolator.InterpolateString(template)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Resource reference"))

				// Verify it's a TemplateError
				templateErr, ok := err.(*TemplateError)
				Expect(ok).To(BeTrue())
				Expect(templateErr.Original).NotTo(BeNil())

				Expect(result).To(Equal(""))
			})
		})
	})

	Describe("InterpolateEnvVars", func() {
		It("interpolates environment variables with templates", func() {
			envVars := map[string]string{
				"SERVICE_URL": "{{ STACKDOME_WEBAPP_PUBLIC }}",
				"API_URL":     "{{ STACKDOME_API_SERVICE_PUBLIC_8000 }}",
				"DB_HOST":     "{{ STACKDOME_WEBAPP_INTERNAL }}",
				"PLAIN_VAR":   "just a string",
			}

			result, err := interpolator.InterpolateEnvVars(envVars)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(4))
			Expect(result["SERVICE_URL"]).To(Equal("https://webapp.example.com"))
			Expect(result["API_URL"]).To(Equal("https://api.example.com"))
			Expect(result["DB_HOST"]).To(Equal("webapp.default.svc.cluster.local"))
			Expect(result["PLAIN_VAR"]).To(Equal("just a string"))
		})

		It("returns errors for invalid templates but continues processing", func() {
			envVars := map[string]string{
				"GOOD_VAR":  "{{ STACKDOME_WEBAPP_PUBLIC }}",
				"BAD_VAR":   "{{ STACKDOME_NONEXISTENT_INTERNAL }}",
				"PLAIN_VAR": "just a string",
			}

			result, err := interpolator.InterpolateEnvVars(envVars)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("interpolation errors"))
			Expect(err.Error()).To(ContainSubstring("BAD_VAR"))
			Expect(result).To(HaveLen(3))
			Expect(result["GOOD_VAR"]).To(Equal("https://webapp.example.com"))
			Expect(result["BAD_VAR"]).To(Equal("{{ STACKDOME_NONEXISTENT_INTERNAL }}")) // Original value preserved
			Expect(result["PLAIN_VAR"]).To(Equal("just a string"))
		})

		It("handles syntax errors in environment variables", func() {
			envVars := map[string]string{
				"GOOD_VAR":     "{{ STACKDOME_WEBAPP_PUBLIC }}",
				"SYNTAX_ERROR": "{{ STACKDOME_WEBAPP_PUBLIC }",
				"PLAIN_VAR":    "just a string",
			}

			result, err := interpolator.InterpolateEnvVars(envVars)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("interpolation errors"))
			Expect(err.Error()).To(ContainSubstring("SYNTAX_ERROR"))
			Expect(err.Error()).To(ContainSubstring("matching '}}' pairs"))
			Expect(result).To(HaveLen(3))
			Expect(result["GOOD_VAR"]).To(Equal("https://webapp.example.com"))
			Expect(result["SYNTAX_ERROR"]).To(Equal("{{ STACKDOME_WEBAPP_PUBLIC }"))
			Expect(result["PLAIN_VAR"]).To(Equal("just a string"))
		})

		It("handles multiple errors in environment variables", func() {
			envVars := map[string]string{
				"GOOD_VAR":     "{{ STACKDOME_WEBAPP_PUBLIC }}",
				"BAD_RESOURCE": "{{ STACKDOME_NONEXISTENT_INTERNAL }}",
				"SYNTAX_ERROR": "{{ STACKDOME_WEBAPP_PUBLIC }",
				"PLAIN_VAR":    "just a string",
			}

			result, err := interpolator.InterpolateEnvVars(envVars)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("interpolation errors"))
			Expect(err.Error()).To(ContainSubstring("BAD_RESOURCE"))
			Expect(err.Error()).To(ContainSubstring("SYNTAX_ERROR"))
			Expect(result).To(HaveLen(4))
			Expect(result["GOOD_VAR"]).To(Equal("https://webapp.example.com"))
			Expect(result["BAD_RESOURCE"]).To(Equal("{{ STACKDOME_NONEXISTENT_INTERNAL }}"))
			Expect(result["SYNTAX_ERROR"]).To(Equal("{{ STACKDOME_WEBAPP_PUBLIC }"))
			Expect(result["PLAIN_VAR"]).To(Equal("just a string"))
		})
	})
})

func stringPtr(s string) *string {
	return &s
}
