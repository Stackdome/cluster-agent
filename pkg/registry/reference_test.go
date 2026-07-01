package registry

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	Expect(registryv1alpha1.AddToScheme(s)).To(Succeed())
	Expect(corev1alpha1.AddToScheme(s)).To(Succeed())
	return s
}

var _ = Describe("ResolvedRepository", func() {
	Describe("Reference", func() {
		It("formats host/repository:tag", func() {
			r := ResolvedRepository{Host: "quay.io", Repository: "myorg/app", Tag: "abc123"}
			Expect(r.Reference()).To(Equal("quay.io/myorg/app:abc123"))
		})
	})
})

var _ = Describe("ResolveTag", func() {
	It("sanitizes the source revision by default", func() {
		Expect(ResolveTag(nil, "ABC/123")).To(Equal("abc-123"))
	})

	It("uses the fixed tag when specified", func() {
		policy := &corev1alpha1.ImageTagPolicy{Fixed: &corev1alpha1.FixedTagPolicy{Tag: "latest"}}
		Expect(ResolveTag(policy, "abc")).To(Equal("latest"))
	})

	It("always sanitizes even with an empty policy", func() {
		policy := &corev1alpha1.ImageTagPolicy{}
		Expect(ResolveTag(policy, "ABC/123")).To(Equal("abc-123"))
	})
})

var _ = Describe("SanitizeTag", func() {
	DescribeTable("converts input to OCI-tag-safe values",
		func(in, want string) {
			Expect(SanitizeTag(in)).To(Equal(want))
		},
		Entry("lowercase passthrough", "abc123", "abc123"),
		Entry("replaces invalid chars with dash", "ABC/123", "abc-123"),
		Entry("trims leading/trailing separators", "---abc---", "abc"),
		Entry("truncates to 128 characters", "a"+strings.Repeat("b", 200), "a"+strings.Repeat("b", 127)),
	)
})

var _ = Describe("ResolveImageRepository", func() {
	Context("with a ClusterRegistryRef", func() {
		It("resolves host from the ClusterRegistry status and marks insecure", func() {
			reg := &registryv1alpha1.ClusterRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: "org-registry", Namespace: "ns"},
			}
			reg.Status.InternalURL = "http://default-registry.stackdome-registry.svc.cluster.local:5000"
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(reg).Build()

			spec := corev1alpha1.ImageRepositorySpec{
				ClusterRegistryRef: &corev1.LocalObjectReference{Name: "org-registry"},
				Repository:         "team/app",
			}
			got, err := ResolveImageRepository(context.Background(), c, "ns", spec, "abc123")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Host).To(Equal("default-registry.stackdome-registry.svc.cluster.local:5000"))
			Expect(got.Repository).To(Equal("team/app"))
			Expect(got.Tag).To(Equal("abc123"))
			Expect(got.Insecure).To(BeTrue())
			Expect(got.Reference()).To(Equal("default-registry.stackdome-registry.svc.cluster.local:5000/team/app:abc123"))
		})
	})

	Context("with an external Docker Hub registry", func() {
		It("uses the host directly and sets the Docker Hub auth URL", func() {
			c := fake.NewClientBuilder().WithScheme(newScheme()).Build()
			spec := corev1alpha1.ImageRepositorySpec{
				External:   &corev1alpha1.ExternalRegistrySpec{Host: "docker.io"},
				Repository: "myorg/app",
			}
			got, err := ResolveImageRepository(context.Background(), c, "ns", spec, "abc")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Reference()).To(Equal("docker.io/myorg/app:abc"))
			Expect(got.Host).To(Equal("docker.io"))
			Expect(got.Insecure).To(BeFalse())
			Expect(got.AuthURL).To(Equal("https://index.docker.io/v1/"))
		})
	})

	Context("with an external insecure registry", func() {
		It("marks the connection as insecure", func() {
			c := fake.NewClientBuilder().WithScheme(newScheme()).Build()
			spec := corev1alpha1.ImageRepositorySpec{
				External: &corev1alpha1.ExternalRegistrySpec{
					Host: "registry.local:5000",
					TLS:  &corev1alpha1.RegistryTLSSpec{Insecure: true},
				},
				Repository: "team/app",
			}
			got, err := ResolveImageRepository(context.Background(), c, "ns", spec, "abc")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Host).To(Equal("registry.local:5000"))
			Expect(got.Reference()).To(Equal("registry.local:5000/team/app:abc"))
			Expect(got.Insecure).To(BeTrue())
			Expect(got.AuthURL).To(Equal("registry.local:5000"))
		})
	})
})
