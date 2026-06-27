package stackresource

import (
	"context"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/mocks"
)

func newSvcTestResource(ports []v1alpha1.Port, annotations map[string]string) *v1alpha1.StackResource {
	return &v1alpha1.StackResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "my-app",
			Namespace:   "test-ns",
			UID:         "test-uid",
			Annotations: annotations,
		},
		Spec: v1alpha1.StackResourceSpec{
			Ports: ports,
		},
	}
}

func expectIngressNotFound(mockClient *mocks.MockClient) {
	mockClient.EXPECT().
		Get(gomock.Any(), client.ObjectKey{Name: "my-app-http-proxy", Namespace: "test-ns"}, gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
		Return(apierrors.NewNotFound(schema.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"}, "my-app-http-proxy"))
}

func expectClusterIssuerGet(mockClient *mocks.MockClient, name string, found bool) {
	call := mockClient.EXPECT().
		Get(gomock.Any(), client.ObjectKey{Name: name}, gomock.AssignableToTypeOf(&cmv1.ClusterIssuer{}))
	if found {
		call.Return(nil)
	} else {
		call.Return(apierrors.NewNotFound(schema.GroupResource{Group: "cert-manager.io", Resource: "clusterissuers"}, name))
	}
}

func middlewareWithSpec(spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "traefik.io/v1alpha1",
			"kind":       "Middleware",
			"metadata": map[string]interface{}{
				"name":      "redirect-https",
				"namespace": "test-ns",
			},
			"spec": spec,
		},
	}
}

func expectMiddlewareExistsWithSpec(mockClient *mocks.MockClient, spec map[string]interface{}) {
	mockClient.EXPECT().
		Get(gomock.Any(), client.ObjectKey{Name: "redirect-https", Namespace: "test-ns"}, gomock.AssignableToTypeOf(&unstructured.Unstructured{})).
		DoAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
			existing := middlewareWithSpec(spec)
			*obj.(*unstructured.Unstructured) = *existing
			return nil
		})
}

func expectMiddlewareCreated(mockClient *mocks.MockClient) {
	mockClient.EXPECT().
		Get(gomock.Any(), client.ObjectKey{Name: "redirect-https", Namespace: "test-ns"}, gomock.AssignableToTypeOf(&unstructured.Unstructured{})).
		Return(apierrors.NewNotFound(schema.GroupResource{Group: "traefik.io", Resource: "middlewares"}, "redirect-https"))

	mockClient.EXPECT().
		Create(gomock.Any(), gomock.AssignableToTypeOf(&unstructured.Unstructured{})).
		DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
			mw := obj.(*unstructured.Unstructured)
			Expect(mw.GetAPIVersion()).To(Equal("traefik.io/v1alpha1"))
			Expect(mw.GetKind()).To(Equal("Middleware"))
			Expect(mw.GetName()).To(Equal("redirect-https"))
			Expect(mw.GetNamespace()).To(Equal("test-ns"))
			spec, _, _ := unstructured.NestedMap(mw.Object, "spec", "redirectScheme")
			Expect(spec["scheme"]).To(Equal("https"))
			Expect(spec["permanent"]).To(Equal(true))
			return nil
		})
}

var _ = Describe("svcReconciler Ingress TLS", func() {
	var (
		mockCtrl   *gomock.Controller
		mockClient *mocks.MockClient
		reconciler *svcReconciler
		ctx        context.Context
		scheme     *runtime.Scheme
		svc        *corev1.Service
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mocks.NewMockClient(mockCtrl)
		ctx = context.Background()

		scheme = runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(networkingv1.AddToScheme(scheme)).To(Succeed())
		Expect(cmv1.AddToScheme(scheme)).To(Succeed())

		reconciler = &svcReconciler{
			Client: mockClient,
			Scheme: scheme,
		}

		svc = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "test-ns"},
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	Context("when port has TLS=true and ClusterIssuer annotation + CR exist", func() {
		It("should add TLS block, cert-manager annotation, and Traefik redirect annotations to the Ingress", func() {
			resource := newSvcTestResource(
				[]v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
				map[string]string{
					v1alpha1.ClusterIssuerAnnotation: "letsencrypt-prod",
				},
			)

			expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
			expectIngressNotFound(mockClient)

			mockClient.EXPECT().
				Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
					ingress := obj.(*networkingv1.Ingress)
					Expect(ingress.Annotations).To(HaveKeyWithValue("cert-manager.io/cluster-issuer", "letsencrypt-prod"))
					Expect(ingress.Annotations).To(HaveKeyWithValue("traefik.ingress.kubernetes.io/router.entrypoints", "web,websecure"))
					Expect(ingress.Annotations).To(HaveKeyWithValue("traefik.ingress.kubernetes.io/router.middlewares", "test-ns-redirect-https@kubernetescrd"))
					Expect(ingress.Spec.TLS).To(HaveLen(1))
					Expect(ingress.Spec.TLS[0].Hosts).To(ConsistOf("app.example.com"))
					Expect(ingress.Spec.TLS[0].SecretName).To(Equal("my-app-tls"))
					return nil
				})
			expectMiddlewareCreated(mockClient)

			portMap, err := reconciler.reconcileIngress(ctx, resource, svc)
			Expect(err).NotTo(HaveOccurred())
			Expect(portMap).To(BeNil())
			cond := findCondition(resource.Status.Conditions, string(v1alpha1.StackResourceTLSConfigured))
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("when port has TLS=false", func() {
		It("should NOT add TLS block or any traefik annotations", func() {
			resource := newSvcTestResource(
				[]v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.local.dev", TLS: false},
				},
				nil,
			)

			expectIngressNotFound(mockClient)

			mockClient.EXPECT().
				Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
					ingress := obj.(*networkingv1.Ingress)
					Expect(ingress.Annotations).NotTo(HaveKey("cert-manager.io/cluster-issuer"))
					Expect(ingress.Annotations).NotTo(HaveKey("traefik.ingress.kubernetes.io/router.entrypoints"))
					Expect(ingress.Annotations).NotTo(HaveKey("traefik.ingress.kubernetes.io/router.middlewares"))
					Expect(ingress.Spec.TLS).To(BeEmpty())
					return nil
				})

			_, err := reconciler.reconcileIngress(ctx, resource, svc)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("when ClusterIssuer annotation is missing from StackResource", func() {
		It("should skip TLS and set TLSReady=False", func() {
			resource := newSvcTestResource(
				[]v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
				map[string]string{},
			)

			expectIngressNotFound(mockClient)

			mockClient.EXPECT().
				Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
					ingress := obj.(*networkingv1.Ingress)
					Expect(ingress.Annotations).NotTo(HaveKey("cert-manager.io/cluster-issuer"))
					Expect(ingress.Annotations).NotTo(HaveKey("traefik.ingress.kubernetes.io/router.entrypoints"))
					Expect(ingress.Annotations).NotTo(HaveKey("traefik.ingress.kubernetes.io/router.middlewares"))
					Expect(ingress.Spec.TLS).To(BeEmpty())
					return nil
				})

			_, err := reconciler.reconcileIngress(ctx, resource, svc)
			Expect(err).NotTo(HaveOccurred())
			cond := findCondition(resource.Status.Conditions, string(v1alpha1.StackResourceTLSConfigured))
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("ClusterIssuerNotConfigured"))
		})
	})

	Context("when ClusterIssuer CR does not exist", func() {
		It("should skip TLS and set TLSReady=False", func() {
			resource := newSvcTestResource(
				[]v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
				map[string]string{
					v1alpha1.ClusterIssuerAnnotation: "letsencrypt-prod",
				},
			)

			expectClusterIssuerGet(mockClient, "letsencrypt-prod", false)
			expectIngressNotFound(mockClient)

			mockClient.EXPECT().
				Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
					ingress := obj.(*networkingv1.Ingress)
					Expect(ingress.Annotations).NotTo(HaveKey("cert-manager.io/cluster-issuer"))
					Expect(ingress.Annotations).NotTo(HaveKey("traefik.ingress.kubernetes.io/router.entrypoints"))
					Expect(ingress.Annotations).NotTo(HaveKey("traefik.ingress.kubernetes.io/router.middlewares"))
					Expect(ingress.Spec.TLS).To(BeEmpty())
					return nil
				})

			_, err := reconciler.reconcileIngress(ctx, resource, svc)
			Expect(err).NotTo(HaveOccurred())
			cond := findCondition(resource.Status.Conditions, string(v1alpha1.StackResourceTLSConfigured))
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("ClusterIssuerNotFound"))
		})
	})

	Context("when multiple ports have different FQDNs with TLS", func() {
		It("should include all FQDNs in the TLS hosts and create rules for each", func() {
			resource := newSvcTestResource(
				[]v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "web.example.com", TLS: true},
					{Name: "api", Number: 9090, Protocol: "http", ExposeToPublic: true, FQDN: "api.example.com", TLS: true},
					{Name: "internal", Number: 3000, Protocol: "http", ExposeToPublic: false},
				},
				map[string]string{
					v1alpha1.ClusterIssuerAnnotation: "letsencrypt-prod",
				},
			)

			expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
			expectIngressNotFound(mockClient)

			mockClient.EXPECT().
				Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
					ingress := obj.(*networkingv1.Ingress)
					Expect(ingress.Annotations).To(HaveKeyWithValue("cert-manager.io/cluster-issuer", "letsencrypt-prod"))
					Expect(ingress.Annotations).To(HaveKeyWithValue("traefik.ingress.kubernetes.io/router.entrypoints", "web,websecure"))
					Expect(ingress.Annotations).To(HaveKeyWithValue("traefik.ingress.kubernetes.io/router.middlewares", "test-ns-redirect-https@kubernetescrd"))
					Expect(ingress.Spec.TLS).To(HaveLen(1))
					Expect(ingress.Spec.TLS[0].Hosts).To(ConsistOf("web.example.com", "api.example.com"))
					Expect(ingress.Spec.TLS[0].SecretName).To(Equal("my-app-tls"))
					Expect(ingress.Spec.Rules).To(HaveLen(2))
					ruleHosts := []string{ingress.Spec.Rules[0].Host, ingress.Spec.Rules[1].Host}
					Expect(ruleHosts).To(ConsistOf("web.example.com", "api.example.com"))
					return nil
				})
			expectMiddlewareCreated(mockClient)

			_, err := reconciler.reconcileIngress(ctx, resource, svc)
			Expect(err).NotTo(HaveOccurred())
			cond := findCondition(resource.Status.Conditions, string(v1alpha1.StackResourceTLSConfigured))
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("when existing Ingress needs TLS update", func() {
		It("should update Ingress when TLS annotation is added", func() {
			resource := newSvcTestResource(
				[]v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
				map[string]string{
					v1alpha1.ClusterIssuerAnnotation: "letsencrypt-prod",
				},
			)

			expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)

			existingIngress := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "my-app-http-proxy",
					Namespace:   "test-ns",
					Annotations: map[string]string{},
				},
				Spec: networkingv1.IngressSpec{
					Rules: []networkingv1.IngressRule{},
				},
			}

			mockClient.EXPECT().
				Get(gomock.Any(), client.ObjectKey{Name: "my-app-http-proxy", Namespace: "test-ns"}, gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				DoAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*networkingv1.Ingress) = *existingIngress
					return nil
				})

			mockClient.EXPECT().
				Update(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
					ingress := obj.(*networkingv1.Ingress)
					Expect(ingress.Annotations).To(HaveKeyWithValue("cert-manager.io/cluster-issuer", "letsencrypt-prod"))
					Expect(ingress.Annotations).To(HaveKeyWithValue("traefik.ingress.kubernetes.io/router.entrypoints", "web,websecure"))
					Expect(ingress.Annotations).To(HaveKeyWithValue("traefik.ingress.kubernetes.io/router.middlewares", "test-ns-redirect-https@kubernetescrd"))
					Expect(ingress.Spec.TLS).To(HaveLen(1))
					return nil
				})
			expectMiddlewareCreated(mockClient)

			_, err := reconciler.reconcileIngress(ctx, resource, svc)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("when redirect Middleware already exists with correct spec", func() {
		It("should not update the Middleware", func() {
			resource := newSvcTestResource(
				[]v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
				map[string]string{
					v1alpha1.ClusterIssuerAnnotation: "letsencrypt-prod",
				},
			)

			expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
			expectIngressNotFound(mockClient)

			mockClient.EXPECT().
				Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				Return(nil)

			expectMiddlewareExistsWithSpec(mockClient, map[string]interface{}{
				"redirectScheme": map[string]interface{}{
					"scheme":    "https",
					"permanent": true,
				},
			})

			_, err := reconciler.reconcileIngress(ctx, resource, svc)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("when redirect Middleware exists with stale spec", func() {
		It("should update the Middleware to the correct spec", func() {
			resource := newSvcTestResource(
				[]v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
				map[string]string{
					v1alpha1.ClusterIssuerAnnotation: "letsencrypt-prod",
				},
			)

			expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
			expectIngressNotFound(mockClient)

			mockClient.EXPECT().
				Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				Return(nil)

			expectMiddlewareExistsWithSpec(mockClient, map[string]interface{}{
				"redirectScheme": map[string]interface{}{
					"scheme":    "http",
					"permanent": false,
				},
			})

			mockClient.EXPECT().
				Update(gomock.Any(), gomock.AssignableToTypeOf(&unstructured.Unstructured{})).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
					mw := obj.(*unstructured.Unstructured)
					spec, _, _ := unstructured.NestedMap(mw.Object, "spec", "redirectScheme")
					Expect(spec["scheme"]).To(Equal("https"))
					Expect(spec["permanent"]).To(Equal(true))
					return nil
				})

			_, err := reconciler.reconcileIngress(ctx, resource, svc)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

var _ = Describe("buildExternalAddresses", func() {
	It("should prefix http:// when TLS is false", func() {
		resource := &v1alpha1.StackResource{
			Spec: v1alpha1.StackResourceSpec{
				Ports: []v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.local.dev", TLS: false},
				},
			},
		}
		addresses := buildExternalAddresses(resource, map[int]string{8080: "app.local.dev"})
		Expect(addresses).To(HaveLen(1))
		Expect(addresses[0].Address).To(Equal("http://app.local.dev"))
		Expect(addresses[0].TargetPort).To(Equal(int32(8080)))
	})

	It("should prefix https:// when TLS is true", func() {
		resource := &v1alpha1.StackResource{
			Spec: v1alpha1.StackResourceSpec{
				Ports: []v1alpha1.Port{
					{Name: "http", Number: 443, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
			},
		}
		addresses := buildExternalAddresses(resource, map[int]string{443: "app.example.com"})
		Expect(addresses).To(HaveLen(1))
		Expect(addresses[0].Address).To(Equal("https://app.example.com"))
	})

	It("should handle mixed TLS and non-TLS ports", func() {
		resource := &v1alpha1.StackResource{
			Spec: v1alpha1.StackResourceSpec{
				Ports: []v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.local.dev", TLS: false},
					{Name: "https", Number: 443, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
			},
		}
		addresses := buildExternalAddresses(resource, map[int]string{
			8080: "app.local.dev",
			443:  "app.example.com",
		})
		Expect(addresses).To(HaveLen(2))
		addrMap := map[string]string{}
		for _, a := range addresses {
			addrMap[a.Address] = a.Address
		}
		Expect(addrMap).To(HaveKey("http://app.local.dev"))
		Expect(addrMap).To(HaveKey("https://app.example.com"))
	})
})

var _ = Describe("address persistence", func() {
	It("reportStackResourceNotReady should not clear addresses", func() {
		resource := &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "my-app",
				Namespace:  "test-ns",
				Generation: 1,
			},
			Status: v1alpha1.StackResourceStatus{
				InternalAddress: strPtr("my-app"),
				ExternalAddress: []v1alpha1.ExternalAddress{
					{TargetPort: 8080, Address: "http://app.local.dev"},
				},
			},
		}

		reportStackResourceNotReady(resource, "DeploymentNotReady", "pods crashing")

		Expect(resource.Status.Phase).To(Equal(v1alpha1.StackResourcePhasePending))
		Expect(resource.Status.InternalAddress).NotTo(BeNil())
		Expect(*resource.Status.InternalAddress).To(Equal("my-app"))
		Expect(resource.Status.ExternalAddress).To(HaveLen(1))
		Expect(resource.Status.ExternalAddress[0].Address).To(Equal("http://app.local.dev"))
	})
})

func strPtr(s string) *string { return &s }

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
