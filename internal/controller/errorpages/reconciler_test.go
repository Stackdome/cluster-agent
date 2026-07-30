package errorpages

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const testNamespace = "stackdome-control-plane"

var traefikGV = schema.GroupVersion{Group: "traefik.io", Version: "v1alpha1"}

func newTestScheme() *runtime.Scheme {
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
	return scheme
}

func newTestClient() client.Client {
	return fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
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

	Context("when the selector parsed to nothing", func() {
		// A Service with a non-nil but empty selector matches every pod in the
		// namespace, so error-page traffic would be load balanced across
		// whatever else happens to run beside the agent. That is worse than no
		// error pages at all, and silent.
		DescribeTable("refuses rather than creating a Service that matches every pod",
			func(empty map[string]string) {
				err := Ensure(ctx, c, testNamespace, empty)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("empty pod selector"))

				svc := &corev1.Service{}
				getErr := c.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: ServiceName}, svc)
				Expect(apierrors.IsNotFound(getErr)).To(BeTrue(), "no Service may be created on the refusal path")
			},
			Entry("nil map", nil),
			Entry("empty map", map[string]string{}),
		)
	})
})

var _ = Describe("EnsureRunnable", func() {
	var (
		ctx      context.Context
		selector map[string]string
	)

	BeforeEach(func() {
		ctx = context.Background()
		selector = map[string]string{"app": "stackdome-operator"}
	})

	It("runs only on the leader, since the objects are cluster-wide singletons", func() {
		runnable, ok := EnsureRunnable(newTestClient(), testNamespace, selector).(manager.LeaderElectionRunnable)
		Expect(ok).To(BeTrue())
		Expect(runnable.NeedLeaderElection()).To(BeTrue())
	})

	It("retries until the objects land, instead of leaving every router unroutable", func() {
		// Two transient API failures stand in for the realistic start-up
		// failures: CRDs not yet established, API server still warming.
		failures := 2
		c := fake.NewClientBuilder().
			WithScheme(newTestScheme()).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(fctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if failures > 0 {
						failures--
						return errors.New("the server is currently unable to handle the request")
					}
					return cl.Create(fctx, obj, opts...)
				},
			}).Build()

		runner := EnsureRunnable(c, testNamespace, selector).(*ensureRunner)
		runner.initialBackoff = time.Millisecond
		runner.maxBackoff = 2 * time.Millisecond

		Expect(runner.Start(ctx)).To(Succeed())

		Expect(failures).To(Equal(0))
		svc := &corev1.Service{}
		Expect(c.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: ServiceName}, svc)).To(Succeed())
	})

	It("gives up quietly when the manager shuts down rather than failing it", func() {
		// A permanently failing cluster must not take the whole agent down: the
		// other controllers are still worth running.
		c := fake.NewClientBuilder().
			WithScheme(newTestScheme()).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					return errors.New("permanently broken")
				},
			}).Build()

		runner := EnsureRunnable(c, testNamespace, selector).(*ensureRunner)
		runner.initialBackoff = time.Millisecond
		runner.maxBackoff = time.Millisecond

		cancelCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- runner.Start(cancelCtx) }()

		Consistently(done, "50ms", "10ms").ShouldNot(Receive(), "must keep retrying while the manager runs")
		cancel()
		Eventually(done, "2s", "10ms").Should(Receive(BeNil()))
	})
})
