package stackresource

import (
	"context"
	"time"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
	"stackdome.io/cluster-agent/internal/controller/mocks"
	"stackdome.io/cluster-agent/pkg/ingresstls"
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

// expectCertificateGet stubs the lookup of the Certificate that cert-manager's ingress-shim
// creates for the resource's Ingress. A nil cert stubs a NotFound.
func expectCertificateGet(mockClient *mocks.MockClient, cert *cmv1.Certificate) {
	call := mockClient.EXPECT().
		Get(gomock.Any(), client.ObjectKey{Name: "my-app-tls", Namespace: "test-ns"}, gomock.AssignableToTypeOf(&cmv1.Certificate{}))
	if cert == nil {
		call.Return(apierrors.NewNotFound(schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}, "my-app-tls"))
		return
	}
	call.DoAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
		*obj.(*cmv1.Certificate) = *cert
		return nil
	})
}

func readyCertificate() *cmv1.Certificate {
	return &cmv1.Certificate{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-tls", Namespace: "test-ns"},
		Status: cmv1.CertificateStatus{
			Conditions: []cmv1.CertificateCondition{{
				Type:   cmv1.CertificateConditionReady,
				Status: cmmeta.ConditionTrue,
			}},
		},
	}
}

// failedCertificate is a Certificate cert-manager has given up on for now:
// FailedIssuanceAttempts is its own count of continuous failures.
func failedCertificate() *cmv1.Certificate {
	return &cmv1.Certificate{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-tls", Namespace: "test-ns"},
		Status: cmv1.CertificateStatus{
			FailedIssuanceAttempts: ptr.To(3),
		},
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
			Expect(spec["permanent"]).To(Equal(false))
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
			expectCertificateGet(mockClient, readyCertificate())
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

			portMap, _, err := reconciler.reconcileIngress(ctx, resource, svc)
			Expect(err).NotTo(HaveOccurred())
			// reconcileIngress returns the port→FQDN map on create too (no requeue).
			Expect(portMap).To(HaveKeyWithValue(8080, "app.example.com"))
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

			_, _, err := reconciler.reconcileIngress(ctx, resource, svc)
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

			_, _, err := reconciler.reconcileIngress(ctx, resource, svc)
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

			_, _, err := reconciler.reconcileIngress(ctx, resource, svc)
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
			expectCertificateGet(mockClient, readyCertificate())
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

			_, _, err := reconciler.reconcileIngress(ctx, resource, svc)
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
			expectCertificateGet(mockClient, readyCertificate())

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

			_, _, err := reconciler.reconcileIngress(ctx, resource, svc)
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
			expectCertificateGet(mockClient, readyCertificate())
			expectIngressNotFound(mockClient)

			mockClient.EXPECT().
				Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				Return(nil)

			expectMiddlewareExistsWithSpec(mockClient, map[string]interface{}{
				"redirectScheme": map[string]interface{}{
					"scheme":    "https",
					"permanent": false,
				},
			})

			_, _, err := reconciler.reconcileIngress(ctx, resource, svc)
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
			expectCertificateGet(mockClient, readyCertificate())
			expectIngressNotFound(mockClient)

			mockClient.EXPECT().
				Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
				Return(nil)

			expectMiddlewareExistsWithSpec(mockClient, map[string]interface{}{
				"redirectScheme": map[string]interface{}{
					"scheme":    "http",
					"permanent": true,
				},
			})

			mockClient.EXPECT().
				Update(gomock.Any(), gomock.AssignableToTypeOf(&unstructured.Unstructured{})).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
					mw := obj.(*unstructured.Unstructured)
					spec, _, _ := unstructured.NestedMap(mw.Object, "spec", "redirectScheme")
					Expect(spec["scheme"]).To(Equal("https"))
					Expect(spec["permanent"]).To(Equal(false))
					return nil
				})

			_, _, err := reconciler.reconcileIngress(ctx, resource, svc)
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
		addresses := buildExternalAddresses(resource, map[int]string{8080: "app.local.dev"}, certIssuanceStageIssuing)
		Expect(addresses).To(HaveLen(1))
		Expect(addresses[0].Address).To(Equal("http://app.local.dev"))
		Expect(addresses[0].TargetPort).To(Equal(int32(8080)))
	})

	It("should prefix https:// when TLS is true and the certificate is ready", func() {
		resource := &v1alpha1.StackResource{
			Spec: v1alpha1.StackResourceSpec{
				Ports: []v1alpha1.Port{
					{Name: "http", Number: 443, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
			},
		}
		addresses := buildExternalAddresses(resource, map[int]string{443: "app.example.com"}, certIssuanceStageReady)
		Expect(addresses).To(HaveLen(1))
		Expect(addresses[0].Address).To(Equal("https://app.example.com"))
	})

	It("should publish nothing for a TLS port while the certificate is still issuing", func() {
		resource := &v1alpha1.StackResource{
			Spec: v1alpha1.StackResourceSpec{
				Ports: []v1alpha1.Port{
					{Name: "http", Number: 443, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
			},
		}
		Expect(buildExternalAddresses(resource, map[int]string{443: "app.example.com"}, certIssuanceStageIssuing)).To(BeEmpty())
	})

	It("should fall back to http:// when no certificate is coming, so the site stays reachable", func() {
		resource := &v1alpha1.StackResource{
			Spec: v1alpha1.StackResourceSpec{
				Ports: []v1alpha1.Port{
					{Name: "http", Number: 443, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
				},
			},
		}
		addresses := buildExternalAddresses(resource, map[int]string{443: "app.example.com"}, certIssuanceStageUnavailable)
		Expect(addresses).To(HaveLen(1))
		Expect(addresses[0].Address).To(Equal("http://app.example.com"))
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
		}, certIssuanceStageReady)
		Expect(addresses).To(HaveLen(2))
		addrMap := map[string]string{}
		for _, a := range addresses {
			addrMap[a.Address] = a.Address
		}
		Expect(addrMap).To(HaveKey("http://app.local.dev"))
		Expect(addrMap).To(HaveKey("https://app.example.com"))
	})
})

var _ = Describe("svcReconciler redirect gating", func() {
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

		reconciler = &svcReconciler{Client: mockClient, Scheme: scheme}
		svc = &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "test-ns"}}
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	tlsResource := func() *v1alpha1.StackResource {
		return newSvcTestResource(
			[]v1alpha1.Port{
				{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
			},
			map[string]string{v1alpha1.ClusterIssuerAnnotation: "letsencrypt-prod"},
		)
	}

	tlsCondition := func(resource *v1alpha1.StackResource) *metav1.Condition {
		return meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceTLSConfigured))
	}

	It("omits the Traefik middleware annotation while the certificate is issuing", func() {
		expectIngressNotFound(mockClient)
		expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
		expectCertificateGet(mockClient, nil)

		mockClient.EXPECT().
			Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
			DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				ingress := obj.(*networkingv1.Ingress)
				// The TLS block must still go on: it is what makes cert-manager's
				// ingress-shim create the Certificate at all.
				Expect(ingress.Spec.TLS).To(HaveLen(1))
				Expect(ingress.Annotations).To(HaveKey(ingresstls.CertManagerClusterIssuerAnnotation))
				Expect(ingress.Annotations).NotTo(HaveKey(ingresstls.TraefikMiddlewaresAnnotation))
				return nil
			})
		expectMiddlewareCreated(mockClient)

		resource := tlsResource()
		_, certState, err := reconciler.reconcileIngress(ctx, resource, svc)
		Expect(err).NotTo(HaveOccurred())
		Expect(certState.Stage).To(Equal(certIssuanceStageIssuing))

		cond := tlsCondition(resource)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("CertificateIssuing"))
	})

	It("adds the Traefik middleware annotation once the certificate is ready", func() {
		expectIngressNotFound(mockClient)
		expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
		expectCertificateGet(mockClient, readyCertificate())

		mockClient.EXPECT().
			Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
			DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				ingress := obj.(*networkingv1.Ingress)
				Expect(ingress.Annotations).To(HaveKey(ingresstls.TraefikMiddlewaresAnnotation))
				return nil
			})
		expectMiddlewareCreated(mockClient)

		resource := tlsResource()
		_, certState, err := reconciler.reconcileIngress(ctx, resource, svc)
		Expect(err).NotTo(HaveOccurred())
		Expect(certState.Stage).To(Equal(certIssuanceStageReady))

		cond := tlsCondition(resource)
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("TLSReady"))
	})

	It("strips a stale redirect annotation when the grace period elapses", func() {
		stale := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-app-http-proxy",
				Namespace: "test-ns",
				Annotations: map[string]string{
					ingresstls.CertManagerClusterIssuerAnnotation: "letsencrypt-prod",
					ingresstls.TraefikMiddlewaresAnnotation:       "test-ns-redirect-https@kubernetescrd",
				},
			},
		}
		expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
		expectCertificateGet(mockClient, nil)
		mockClient.EXPECT().
			Get(gomock.Any(), client.ObjectKey{Name: "my-app-http-proxy", Namespace: "test-ns"}, gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
			DoAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				*obj.(*networkingv1.Ingress) = *stale
				return nil
			})

		mockClient.EXPECT().
			Update(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
			DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				ingress := obj.(*networkingv1.Ingress)
				Expect(ingress.Annotations).NotTo(HaveKey(ingresstls.TraefikMiddlewaresAnnotation))
				return nil
			})
		expectMiddlewareCreated(mockClient)

		// The grace clock is TLSConfigured's LastTransitionTime, so an old pending
		// condition is what puts this resource past the deadline.
		resource := tlsResource()
		resource.Status.Conditions = []metav1.Condition{{
			Type:               string(v1alpha1.StackResourceTLSConfigured),
			Status:             metav1.ConditionFalse,
			Reason:             "CertificateIssuing",
			LastTransitionTime: metav1.NewTime(time.Now().Add(-2 * certGracePeriod)),
		}}

		_, certState, err := reconciler.reconcileIngress(ctx, resource, svc)
		Expect(err).NotTo(HaveOccurred())
		Expect(certState.Stage).To(Equal(certIssuanceStageUnavailable))
		Expect(tlsCondition(resource).Reason).To(Equal("CertificateTimedOut"))
	})
})

var _ = Describe("svcReconciler convergence gating on TLS issuance", func() {
	var (
		mockCtrl   *gomock.Controller
		mockClient *mocks.MockClient
		reconciler *svcReconciler
		ctx        context.Context
		verdicts   *controller.VerdictCollector
		scheme     *runtime.Scheme
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mocks.NewMockClient(mockCtrl)
		ctx, verdicts = controller.ContextWithVerdicts(context.Background())

		scheme = runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(networkingv1.AddToScheme(scheme)).To(Succeed())
		Expect(cmv1.AddToScheme(scheme)).To(Succeed())

		reconciler = &svcReconciler{Client: mockClient, Scheme: scheme}
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	expectServiceCreated := func() {
		mockClient.EXPECT().
			Get(gomock.Any(), client.ObjectKey{Name: "my-app", Namespace: "test-ns"}, gomock.AssignableToTypeOf(&corev1.Service{})).
			Return(apierrors.NewNotFound(schema.GroupResource{Resource: "services"}, "my-app"))
		mockClient.EXPECT().
			Create(gomock.Any(), gomock.AssignableToTypeOf(&corev1.Service{})).
			Return(nil)
	}

	expectIngressCreated := func() {
		expectIngressNotFound(mockClient)
		mockClient.EXPECT().
			Create(gomock.Any(), gomock.AssignableToTypeOf(&networkingv1.Ingress{})).
			Return(nil)
	}

	// tlsResource is a public port with TLS: the only shape the gate applies to.
	tlsResource := func() *v1alpha1.StackResource {
		resource := newSvcTestResource(
			[]v1alpha1.Port{
				{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.example.com", TLS: true},
			},
			map[string]string{v1alpha1.ClusterIssuerAnnotation: "letsencrypt-prod"},
		)
		resource.Generation = 1
		// The workload itself has rolled out: without the TLS gate the summary
		// would mirror this and report Converged=True with no published URL.
		resource.Status.Conditions = []metav1.Condition{{
			Type:               string(v1alpha1.StackResourceWorkloadConverged),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
			Reason:             "WorkloadConverged",
			Message:            "rollout complete",
		}}
		return resource
	}

	convergedCondition := func(resource *v1alpha1.StackResource) *metav1.Condition {
		deriveSummaryStatus(resource, verdicts)
		return findCondition(resource.Status.Conditions, string(v1alpha1.StackResourceConverged))
	}

	It("stays not converged while the certificate is still issuing inside the grace period", func() {
		expectServiceCreated()
		expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
		expectCertificateGet(mockClient, nil)
		expectIngressCreated()
		expectMiddlewareCreated(mockClient)

		resource := tlsResource()
		res, err := reconciler.reconcile(ctx, resource)
		Expect(err).NotTo(HaveOccurred())

		By("scheduling the pass that will see the grace period expire")
		Expect(res.DeferredRequeueAfter).NotTo(BeNil())
		Expect(*res.DeferredRequeueAfter).To(BeNumerically(">", 0))
		Expect(*res.DeferredRequeueAfter).To(BeNumerically("<=", certGracePeriod))

		By("publishing no address for the TLS port")
		Expect(resource.Status.ExternalAddress).To(BeEmpty())

		By("gating Converged even though the workload converged")
		Expect(verdicts.NotReady()).NotTo(BeNil())
		cond := convergedCondition(resource)
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("CertificateIssuing"))
	})

	It("converges with an https address once the certificate is issued", func() {
		expectServiceCreated()
		expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
		expectCertificateGet(mockClient, readyCertificate())
		expectIngressCreated()
		expectMiddlewareCreated(mockClient)

		resource := tlsResource()
		res, err := reconciler.reconcile(ctx, resource)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(resultNil))
		Expect(verdicts.NotReady()).To(BeNil())

		Expect(resource.Status.ExternalAddress).To(HaveLen(1))
		Expect(resource.Status.ExternalAddress[0].Address).To(Equal("https://app.example.com"))
		Expect(convergedCondition(resource).Status).To(Equal(metav1.ConditionTrue))
	})

	It("converges with an http address when issuance terminally fails", func() {
		expectServiceCreated()
		expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
		expectCertificateGet(mockClient, failedCertificate())
		expectIngressCreated()
		expectMiddlewareCreated(mockClient)

		resource := tlsResource()
		res, err := reconciler.reconcile(ctx, resource)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(resultNil))
		Expect(verdicts.NotReady()).To(BeNil())

		Expect(resource.Status.ExternalAddress).To(HaveLen(1))
		Expect(resource.Status.ExternalAddress[0].Address).To(Equal("http://app.example.com"))
		Expect(convergedCondition(resource).Status).To(Equal(metav1.ConditionTrue))
	})

	It("converges with an http address once the grace period expires", func() {
		expectServiceCreated()
		expectClusterIssuerGet(mockClient, "letsencrypt-prod", true)
		expectCertificateGet(mockClient, nil)
		expectIngressCreated()
		expectMiddlewareCreated(mockClient)

		resource := tlsResource()
		resource.Status.Conditions = append(resource.Status.Conditions, metav1.Condition{
			Type:               string(v1alpha1.StackResourceTLSConfigured),
			Status:             metav1.ConditionFalse,
			Reason:             "CertificateIssuing",
			LastTransitionTime: metav1.NewTime(time.Now().Add(-2 * certGracePeriod)),
		})

		res, err := reconciler.reconcile(ctx, resource)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(resultNil))
		Expect(verdicts.NotReady()).To(BeNil())

		Expect(resource.Status.ExternalAddress).To(HaveLen(1))
		Expect(resource.Status.ExternalAddress[0].Address).To(Equal("http://app.example.com"))
		Expect(convergedCondition(resource).Status).To(Equal(metav1.ConditionTrue))
	})
})

var _ = Describe("certificateToStackResource", func() {
	It("maps a <name>-tls Certificate back to the StackResource <name>", func() {
		cert := &cmv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{Name: "my-app-tls", Namespace: "test-ns"},
		}
		requests := certificateToStackResource(context.Background(), cert)
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Name).To(Equal("my-app"))
		Expect(requests[0].Namespace).To(Equal("test-ns"))
	})

	It("ignores a Certificate that does not follow the naming rule", func() {
		cert := &cmv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{Name: "some-other-cert", Namespace: "test-ns"},
		}
		Expect(certificateToStackResource(context.Background(), cert)).To(BeEmpty())
	})

	It("ignores an object that is not a Certificate", func() {
		Expect(certificateToStackResource(context.Background(), &corev1.Service{})).To(BeEmpty())
	})
})

var _ = Describe("address persistence", func() {
	It("a not-ready derivation should not clear addresses", func() {
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

		verdicts := &controller.VerdictCollector{}
		verdicts.ReportNotReady("DeploymentNotReady", "pods crashing")
		deriveSummaryStatus(resource, verdicts)

		Expect(resource.Status.Phase).To(Equal(v1alpha1.StackResourcePhasePending))
		Expect(resource.Status.InternalAddress).NotTo(BeNil())
		Expect(*resource.Status.InternalAddress).To(Equal("my-app"))
		Expect(resource.Status.ExternalAddress).To(HaveLen(1))
		Expect(resource.Status.ExternalAddress[0].Address).To(Equal("http://app.local.dev"))
	})
})

func strPtr(s string) *string { return &s }

var _ = Describe("svcReconciler.reconcile (orchestration)", func() {
	var (
		reconciler *svcReconciler
		fakeClient client.Client
		scheme     *runtime.Scheme
		ctx        context.Context
		verdicts   *controller.VerdictCollector
	)

	BeforeEach(func() {
		ctx, verdicts = controller.ContextWithVerdicts(context.Background())
		scheme = runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(networkingv1.AddToScheme(scheme)).To(Succeed())

		fakeClient = fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler = &svcReconciler{Client: fakeClient, Scheme: scheme}
	})

	orchestrationResource := func(workloadType v1alpha1.WorkloadType, ports ...v1alpha1.Port) *v1alpha1.StackResource {
		return &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-app",
				Namespace: "test-ns",
				UID:       "test-uid",
				Labels:    map[string]string{v1alpha1.LabelManagedBy: "stackdome"},
			},
			Spec: v1alpha1.StackResourceSpec{WorkloadType: workloadType, Ports: ports},
		}
	}

	serviceExists := func() bool {
		err := fakeClient.Get(ctx, client.ObjectKey{Name: "my-app", Namespace: "test-ns"}, &corev1.Service{})
		return err == nil
	}

	// The svc reconciler contributes domain conditions and verdicts only; a
	// clean pass must file neither verdict, so the derivation is left free to
	// follow the workload's facts.
	expectNoVerdicts := func() {
		Expect(verdicts.NotReady()).To(BeNil())
		Expect(verdicts.Failed()).To(BeNil())
	}

	It("skips Service/Ingress for a Worker", func() {
		resource := orchestrationResource(v1alpha1.WorkloadTypeWorker)

		res, err := reconciler.reconcile(ctx, resource)

		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(resultNil))
		Expect(serviceExists()).To(BeFalse(), "Worker should not get a Service")
		Expect(resource.Status.InternalAddress).To(BeNil())
		expectNoVerdicts()
	})

	It("creates the Service in one pass (no exposed ports)", func() {
		// Internal-only port: a Service is created, but no Ingress is needed. ensureService
		// returns the just-created Service, so reconcile publishes the internal address in
		// one pass.
		resource := orchestrationResource(v1alpha1.WorkloadTypeService,
			v1alpha1.Port{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: false})

		res, err := reconciler.reconcile(ctx, resource)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(resultNil))
		Expect(serviceExists()).To(BeTrue())
		Expect(resource.Status.InternalAddress).NotTo(BeNil())
		Expect(*resource.Status.InternalAddress).To(Equal("my-app"))
		Expect(findCondition(resource.Status.Conditions, string(v1alpha1.StackResourceIngressReady))).To(BeNil(),
			"no Ingress is created for an internal-only port")
		expectNoVerdicts()

		By("second reconcile is idempotent (Service already up to date)")
		res, err = reconciler.reconcile(ctx, resource)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(resultNil))
		Expect(*resource.Status.InternalAddress).To(Equal("my-app"))
		expectNoVerdicts()
	})

	It("creates Service+Ingress in one pass (exposed port, no TLS)", func() {
		resource := orchestrationResource(v1alpha1.WorkloadTypeService,
			v1alpha1.Port{Name: "http", Number: 8080, Protocol: "http", ExposeToPublic: true, FQDN: "app.local.dev"})

		res, err := reconciler.reconcile(ctx, resource)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(resultNil))
		expectNoVerdicts()

		By("Service and Ingress both created in the same pass")
		Expect(serviceExists()).To(BeTrue())
		ing := &networkingv1.Ingress{}
		Expect(fakeClient.Get(ctx, client.ObjectKey{Name: "my-app-http-proxy", Namespace: "test-ns"}, ing)).To(Succeed())
		Expect(ing.Spec.Rules).To(HaveLen(1))
		Expect(ing.Spec.Rules[0].Host).To(Equal("app.local.dev"))

		By("external address published and IngressReady set without a requeue")
		ingressCond := findCondition(resource.Status.Conditions, string(v1alpha1.StackResourceIngressReady))
		Expect(ingressCond).NotTo(BeNil())
		Expect(ingressCond.Status).To(Equal(metav1.ConditionTrue))
		Expect(resource.Status.ExternalAddress).To(HaveLen(1))
		Expect(resource.Status.ExternalAddress[0].Address).To(Equal("http://app.local.dev"))
		Expect(resource.Status.ExternalAddress[0].TargetPort).To(Equal(int32(8080)))

		By("second reconcile is idempotent")
		res, err = reconciler.reconcile(ctx, resource)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(resultNil))
		Expect(resource.Status.ExternalAddress).To(HaveLen(1))
		expectNoVerdicts()
	})

	It("creates a headless Service for a StatefulService", func() {
		resource := orchestrationResource(v1alpha1.WorkloadTypeStatefulService)

		_, err := reconciler.reconcile(ctx, resource)
		Expect(err).NotTo(HaveOccurred())

		svc := &corev1.Service{}
		Expect(fakeClient.Get(ctx, client.ObjectKey{Name: "my-app", Namespace: "test-ns"}, svc)).To(Succeed())
		Expect(svc.Spec.ClusterIP).To(Equal("None"))
	})
})

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

func svcShapeResource(ports ...v1alpha1.Port) *v1alpha1.StackResource {
	return &v1alpha1.StackResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shape-test", Namespace: "test-ns",
			Labels: map[string]string{v1alpha1.LabelManagedBy: "stackdome"},
		},
		Spec: v1alpha1.StackResourceSpec{WorkloadType: v1alpha1.WorkloadTypeService, Ports: ports},
	}
}

var _ = Describe("buildDesiredService shape (regression guard)", func() {
	It("is headless (ClusterIP=None) when the resource has no ports", func() {
		svc := (&svcReconciler{}).buildDesiredService(svcShapeResource())
		Expect(svc.Spec.ClusterIP).To(Equal("None"))
		Expect(svc.Spec.Ports).To(BeEmpty())
	})
	It("is ClusterIP with ports when the resource declares ports", func() {
		svc := (&svcReconciler{}).buildDesiredService(svcShapeResource(v1alpha1.Port{Name: "http", Number: 8080, Protocol: "http"}))
		Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
		Expect(svc.Spec.Ports).To(HaveLen(1))
		Expect(svc.Spec.Selector).To(Equal(map[string]string{"resource": "shape-test"}))
	})
})

var _ = Describe("buildServicePorts", func() {
	It("maps each spec port to a ServicePort with the name as target port", func() {
		resource := &v1alpha1.StackResource{
			Spec: v1alpha1.StackResourceSpec{
				Ports: []v1alpha1.Port{
					{Name: "http", Number: 8080, Protocol: "http"},
					{Name: "grpc", Number: 9090, Protocol: "grpc"},
				},
			},
		}
		ports := buildServicePorts(resource)
		Expect(ports).To(HaveLen(2))
		Expect(ports[0].Name).To(Equal("http"))
		Expect(ports[0].Port).To(Equal(int32(8080)))
		Expect(ports[0].TargetPort.StrVal).To(Equal("http"))
		Expect(ports[1].Name).To(Equal("grpc"))
		Expect(ports[1].Port).To(Equal(int32(9090)))
	})

	It("returns an empty slice when there are no ports", func() {
		Expect(buildServicePorts(&v1alpha1.StackResource{})).To(BeEmpty())
	})
})

var _ = Describe("buildDesiredIngress", func() {
	It("constructs the Ingress from the supplied rules, annotations, and TLS", func() {
		resource := &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "test-ns"},
		}
		rules := buildIngressRules(map[int]string{8080: "app.example.com"}, "my-app")
		annotations := map[string]string{"cert-manager.io/cluster-issuer": "letsencrypt-prod"}
		tls := []networkingv1.IngressTLS{{Hosts: []string{"app.example.com"}, SecretName: "my-app-tls"}}

		ing := buildDesiredIngress(resource, rules, annotations, tls)

		Expect(ing.Name).To(Equal("my-app-http-proxy"))
		Expect(ing.Namespace).To(Equal("test-ns"))
		Expect(ing.Annotations).To(HaveKeyWithValue("cert-manager.io/cluster-issuer", "letsencrypt-prod"))
		Expect(ing.Spec.TLS).To(Equal(tls))
		Expect(ing.Spec.Rules).To(HaveLen(1))
		Expect(ing.Spec.Rules[0].Host).To(Equal("app.example.com"))
	})
})

var _ = Describe("isHeadlessService", func() {
	It("is headless for a StatefulService even with ports", func() {
		res := &v1alpha1.StackResource{Spec: v1alpha1.StackResourceSpec{WorkloadType: v1alpha1.WorkloadTypeStatefulService}}
		Expect(isHeadlessService(res, []corev1.ServicePort{{Name: "http"}})).To(BeTrue())
	})
	It("is headless for any workload with no ports", func() {
		res := &v1alpha1.StackResource{Spec: v1alpha1.StackResourceSpec{WorkloadType: v1alpha1.WorkloadTypeService}}
		Expect(isHeadlessService(res, nil)).To(BeTrue())
	})
	It("is not headless for a Service with ports", func() {
		res := &v1alpha1.StackResource{Spec: v1alpha1.StackResourceSpec{WorkloadType: v1alpha1.WorkloadTypeService}}
		Expect(isHeadlessService(res, []corev1.ServicePort{{Name: "http"}})).To(BeFalse())
	})
})

var _ = Describe("svcReconciler workload type handling", func() {
	It("StatefulService with ports gets headless service with ports", func() {
		res := &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{
				Name: "sts-app", Namespace: "test-ns",
				Labels: map[string]string{v1alpha1.LabelManagedBy: "stackdome"},
			},
			Spec: v1alpha1.StackResourceSpec{
				WorkloadType: v1alpha1.WorkloadTypeStatefulService,
				Ports:        []v1alpha1.Port{{Name: "http", Number: 8080, Protocol: "http"}},
			},
		}
		svc := (&svcReconciler{}).buildDesiredService(res)
		Expect(svc.Spec.ClusterIP).To(Equal("None"))
		Expect(svc.Spec.Ports).To(HaveLen(1))
	})

	It("StatefulService without ports gets headless service without ports", func() {
		res := &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{
				Name: "sts-worker", Namespace: "test-ns",
				Labels: map[string]string{v1alpha1.LabelManagedBy: "stackdome"},
			},
			Spec: v1alpha1.StackResourceSpec{
				WorkloadType: v1alpha1.WorkloadTypeStatefulService,
			},
		}
		svc := (&svcReconciler{}).buildDesiredService(res)
		Expect(svc.Spec.ClusterIP).To(Equal("None"))
		Expect(svc.Spec.Ports).To(BeEmpty())
	})
})
