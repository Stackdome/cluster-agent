package errorpages

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testNamespace = "stackdome-control-plane"

var traefikGV = schema.GroupVersion{Group: "traefik.io", Version: "v1alpha1"}

func newTestClient() client.Client {
	scheme := runtime.NewScheme()
	Expect(corev1.AddToScheme(scheme)).To(Succeed())

	for _, kind := range []string{"Middleware", "IngressRoute"} {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(traefikGV.WithKind(kind))
		scheme.AddKnownTypeWithName(traefikGV.WithKind(kind), obj)

		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(traefikGV.WithKind(kind + "List"))
		scheme.AddKnownTypeWithName(traefikGV.WithKind(kind+"List"), list)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

var _ = Describe("Ensure", func() {
	var (
		ctx      context.Context
		c        client.Client
		selector map[string]string
	)

	BeforeEach(func() {
		ctx = context.Background()
		c = newTestClient()
		selector = map[string]string{"app": "stackdome-operator"}
	})

	getTraefik := func(kind, name string) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(traefikGV.WithKind(kind))
		Expect(c.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: name}, obj)).To(Succeed())
		return obj
	}

	Context("on a cluster with none of the objects", func() {
		BeforeEach(func() {
			Expect(Ensure(ctx, c, testNamespace, selector)).To(Succeed())
		})

		It("creates a Service pointing at the agent pod", func() {
			svc := &corev1.Service{}
			Expect(c.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "stackdome-error-pages"}, svc)).To(Succeed())
			Expect(svc.Spec.Selector).To(Equal(selector))
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(80)))
		})

		It("creates the errors middleware covering all 5xx statuses", func() {
			mw := getTraefik("Middleware", "stackdome-errors")

			status, found, err := unstructured.NestedStringSlice(mw.Object, "spec", "errors", "status")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(status).To(ConsistOf("500-599"))

			query, _, err := unstructured.NestedString(mw.Object, "spec", "errors", "query")
			Expect(err).NotTo(HaveOccurred())
			Expect(query).To(Equal("/{status}"))
		})

		It("creates a catch-all route at the lowest priority", func() {
			// Priority must lose to every real stack route, or it would swallow
			// live traffic.
			route := getTraefik("IngressRoute", "stackdome-catch-all")

			routes, found, err := unstructured.NestedSlice(route.Object, "spec", "routes")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(routes).To(HaveLen(1))
			Expect(routes[0].(map[string]interface{})["priority"]).To(BeEquivalentTo(1))
		})
	})

	Context("when called repeatedly", func() {
		It("succeeds and does not duplicate objects", func() {
			Expect(Ensure(ctx, c, testNamespace, selector)).To(Succeed())
			Expect(Ensure(ctx, c, testNamespace, selector)).To(Succeed())

			list := &corev1.ServiceList{}
			Expect(c.List(ctx, list, client.InNamespace(testNamespace))).To(Succeed())
			Expect(list.Items).To(HaveLen(1))
		})
	})
})
