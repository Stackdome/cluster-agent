package stackresource

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

type listRecordingClient struct {
	client.Client
	lastList client.ObjectList
}

func (c *listRecordingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.lastList = list
	return c.Client.List(ctx, list, opts...)
}

var _ = Describe("referenced TLS Secret watch", func() {
	var (
		ctx        context.Context
		reconciler *StackResourceReconciler
	)

	referencingResource := func(name, namespace, ref string, usesPlatformWildcardTLS bool) *v1alpha1.StackResource {
		resource := newSvcTestResource([]v1alpha1.Port{{
			Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true,
			FQDN: name + ".stackdome.app", TLS: true, TLSSecretRef: ref,
		}}, nil)
		resource.Name = name
		resource.Namespace = namespace
		if usesPlatformWildcardTLS {
			resource.Labels = map[string]string{v1alpha1.LabelUsesPlatformWildcardTLS: "true"}
		}
		return resource
	}

	platformWildcardSecret := func(namespace string) *corev1.Secret {
		secret := sourceTLSSecret(namespace, sourceSecretName, "first")
		secret.Labels = map[string]string{v1alpha1.LabelPlatformWildcardTLSSecret: "true"}
		return secret
	}

	BeforeEach(func() {
		ctx = context.Background()
		scheme := runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())

		baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			referencingResource("uses-platform-wildcard", "test-ns", referencedTLSSecretRef, true),
			referencingResource("same-reference-without-label", "test-ns", referencedTLSSecretRef, false),
			referencingResource("uses-something-else", "test-ns", "other-tls", false),
		).Build()
		recordingClient := &listRecordingClient{Client: baseClient}
		reconciler = &StackResourceReconciler{
			Client:               recordingClient,
			uncachedClient:       recordingClient,
			platformTLSNamespace: sourceSecretNamespace,
			Scheme:               scheme,
		}
	})

	It("enqueues only labeled wildcard consumers", func() {
		requests := reconciler.platformWildcardTLSSecretToStackResources(ctx, platformWildcardSecret(sourceSecretNamespace))
		Expect(requests).To(Equal([]reconcile.Request{{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "uses-platform-wildcard"},
		}}))
	})

	It("lists only StackResource metadata", func() {
		reconciler.platformWildcardTLSSecretToStackResources(ctx, platformWildcardSecret(sourceSecretNamespace))
		recordingClient := reconciler.uncachedClient.(*listRecordingClient)
		Expect(recordingClient.lastList).To(BeAssignableToTypeOf(&metav1.PartialObjectMetadataList{}))
	})

	It("ignores a wildcard Secret outside the configured control-plane namespace", func() {
		Expect(reconciler.platformWildcardTLSSecretToStackResources(ctx, platformWildcardSecret("other-control-plane"))).To(BeEmpty())
	})

	It("ignores an unmarked TLS Secret", func() {
		Expect(reconciler.platformWildcardTLSSecretToStackResources(ctx, sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first"))).To(BeEmpty())
	})

	It("ignores a Secret that is not TLS", func() {
		opaque := sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first")
		opaque.Type = corev1.SecretTypeOpaque
		Expect(reconciler.platformWildcardTLSSecretToStackResources(ctx, opaque)).To(BeEmpty())
	})
})
