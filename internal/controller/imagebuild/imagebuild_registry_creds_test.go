package imagebuild

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
)

func imageBuildScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	Expect(registryv1alpha1.AddToScheme(scheme)).To(Succeed())
	Expect(corev1.AddToScheme(scheme)).To(Succeed())
	return scheme
}

// htpasswdRegistry is a ClusterRegistry whose credentials live in a secret in
// the registry namespace (independent of any StackResource).
func htpasswdRegistry() *registryv1alpha1.ClusterRegistry {
	return &registryv1alpha1.ClusterRegistry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg"},
		Spec: registryv1alpha1.ClusterRegistrySpec{
			Auth: &registryv1alpha1.RegistryAuthSpec{
				HtPasswordCredentials: &registryv1alpha1.HtPasswordCredentialsSpec{
					CredentialsRef: &corev1alpha1.CredentialSecretKeyPair{
						SecretRef:   corev1.SecretReference{Name: "reg-creds", Namespace: "reg-ns"},
						UsernameKey: "username",
						PasswordKey: "password",
					},
				},
			},
		},
	}
}

var _ = Describe("registryCredentials", func() {
	var scheme *runtime.Scheme

	BeforeEach(func() { scheme = imageBuildScheme() })

	newReconciler := func(objs ...client.Object) *ImageBuildReconciler {
		fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
		return &ImageBuildReconciler{Client: fc, Scheme: scheme}
	}

	It("returns htpasswd credentials from the ClusterRegistry's own secret", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "reg-creds", Namespace: "reg-ns"},
			Data:       map[string][]byte{"username": []byte("alice"), "password": []byte("s3cret")},
		}
		r := newReconciler(secret)

		u, p, err := r.registryCredentials(context.Background(), htpasswdRegistry())
		Expect(err).NotTo(HaveOccurred())
		Expect(u).To(Equal("alice"))
		Expect(p).To(Equal("s3cret"))
	})

	It("returns anonymous when the registry has no auth configured", func() {
		r := newReconciler()

		u, p, err := r.registryCredentials(context.Background(), &registryv1alpha1.ClusterRegistry{})
		Expect(err).NotTo(HaveOccurred())
		Expect(u).To(BeEmpty())
		Expect(p).To(BeEmpty())
	})

	It("returns anonymous (no error) when the credentials secret is missing", func() {
		// Registry references reg-creds, but the secret is not seeded — e.g. the
		// registry namespace is being torn down. Must not block deletion.
		r := newReconciler()

		u, p, err := r.registryCredentials(context.Background(), htpasswdRegistry())
		Expect(err).NotTo(HaveOccurred())
		Expect(u).To(BeEmpty())
		Expect(p).To(BeEmpty())
	})
})

var _ = Describe("cleanupImage", func() {
	var scheme *runtime.Scheme

	BeforeEach(func() { scheme = imageBuildScheme() })

	newReconciler := func(objs ...client.Object) *ImageBuildReconciler {
		fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
		return &ImageBuildReconciler{Client: fc, Scheme: scheme}
	}

	imageBuild := func(clusterRef *corev1.LocalObjectReference, imageURL string) *buildsv1alpha1.ImageBuild {
		return &buildsv1alpha1.ImageBuild{
			ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "app-ns"},
			Spec: buildsv1alpha1.ImageBuildSpec{
				Repository: corev1alpha1.ImageRepositorySpec{
					ClusterRegistryRef: clusterRef,
					Repository:         "app/app",
				},
			},
			Status: buildsv1alpha1.ImageBuildStatus{ImageUrl: imageURL},
		}
	}

	It("is a no-op for external registries (no ClusterRegistryRef)", func() {
		r := newReconciler()
		Expect(r.cleanupImage(context.Background(),
			imageBuild(nil, "reg.svc/app/app:tag"))).To(Succeed())
	})

	It("is a no-op when the build never pushed (empty imageUrl)", func() {
		r := newReconciler()
		Expect(r.cleanupImage(context.Background(),
			imageBuild(&corev1.LocalObjectReference{Name: "reg"}, ""))).To(Succeed())
	})

	It("is a no-op when the ClusterRegistry is gone (teardown in progress)", func() {
		// No ClusterRegistry seeded → Get returns NotFound → skip, do not block.
		r := newReconciler()
		Expect(r.cleanupImage(context.Background(),
			imageBuild(&corev1.LocalObjectReference{Name: "reg"}, "reg.svc/app/app:tag"))).To(Succeed())
	})
})
