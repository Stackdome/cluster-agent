package clusterinfo_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	networkv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8sapierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/clusterinfo"
	"stackdome.io/cluster-agent/internal/controller/mocks"
)

func TestClusterInfoController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ClusterInfo Controller Suite")
}

// mockSubResourceWriter is a manual mock for client.SubResourceWriter.
type mockSubResourceWriter struct {
	patchErr error
}

func (m *mockSubResourceWriter) Create(_ context.Context, _ client.Object, _ client.Object, _ ...client.SubResourceCreateOption) error {
	return nil
}
func (m *mockSubResourceWriter) Update(_ context.Context, _ client.Object, _ ...client.SubResourceUpdateOption) error {
	return nil
}
func (m *mockSubResourceWriter) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	return m.patchErr
}

var _ = Describe("ClusterInfoReconciler", func() {
	var (
		mockCtrl   *gomock.Controller
		mockClient *mocks.MockClient
		reconciler *clusterinfo.ClusterInfoReconciler
		ctx        context.Context
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mocks.NewMockClient(mockCtrl)
		ctx = context.Background()
		reconciler = &clusterinfo.ClusterInfoReconciler{
			Client: mockClient,
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	Describe("Reconcile", func() {
		Context("when the ClusterInfo CR does not exist", func() {
			It("creates the singleton and returns without error", func() {
				mockClient.EXPECT().
					Get(ctx, types.NamespacedName{Name: corev1alpha1.ClusterInfoSingletonName}, gomock.AssignableToTypeOf(&corev1alpha1.ClusterInfo{})).
					Return(k8sapierrors.NewNotFound(schema.GroupResource{}, corev1alpha1.ClusterInfoSingletonName))

				mockClient.EXPECT().
					Create(ctx, gomock.AssignableToTypeOf(&corev1alpha1.ClusterInfo{})).
					DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
						ci := obj.(*corev1alpha1.ClusterInfo)
						Expect(ci.Name).To(Equal(corev1alpha1.ClusterInfoSingletonName))
						return nil
					})

				result, err := reconciler.Reconcile(ctx, ctrl.Request{
					NamespacedName: types.NamespacedName{Name: corev1alpha1.ClusterInfoSingletonName},
				})

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))
			})
		})

		Context("when the ClusterInfo CR exists and all Linkerd checks pass", func() {
			It("collects status and patches", func() {
				existingCR := &corev1alpha1.ClusterInfo{
					ObjectMeta: metav1.ObjectMeta{Name: corev1alpha1.ClusterInfoSingletonName},
				}

				mockClient.EXPECT().
					Get(ctx, types.NamespacedName{Name: corev1alpha1.ClusterInfoSingletonName}, gomock.AssignableToTypeOf(&corev1alpha1.ClusterInfo{})).
					DoAndReturn(func(_ context.Context, _ types.NamespacedName, obj *corev1alpha1.ClusterInfo, _ ...client.GetOption) error {
						*obj = *existingCR
						return nil
					})

				mockClient.EXPECT().
					List(ctx, gomock.AssignableToTypeOf(&corev1.NodeList{}), gomock.Any()).
					DoAndReturn(func(_ context.Context, list *corev1.NodeList, _ ...client.ListOption) error {
						list.Items = []corev1.Node{}
						return nil
					})

				mockClient.EXPECT().
					List(ctx, gomock.AssignableToTypeOf(&storagev1.StorageClassList{}), gomock.Any()).
					Return(nil)

				mockClient.EXPECT().
					List(ctx, gomock.AssignableToTypeOf(&corev1.ServiceList{}), gomock.Any()).
					Return(nil)

				mockClient.EXPECT().
					List(ctx, gomock.AssignableToTypeOf(&networkv1.IngressClassList{}), gomock.Any()).
					Return(nil)

				mockClient.EXPECT().Status().Return(&mockSubResourceWriter{})

				_, err := reconciler.Reconcile(ctx, ctrl.Request{
					NamespacedName: types.NamespacedName{Name: corev1alpha1.ClusterInfoSingletonName},
				})

				Expect(err).NotTo(HaveOccurred())
			})
		})
	})
})

var _ = Describe("collectors", func() {
	Describe("BuildNodeInfoList", func() {
		It("maps allocatable resources and topology from node labels", func() {
			node := corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Labels: map[string]string{
						"topology.kubernetes.io/zone":   "us-east-1a",
						"topology.kubernetes.io/region": "us-east-1",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:              resource.MustParse("3800m"),
						corev1.ResourceMemory:           resource.MustParse("7Gi"),
						corev1.ResourceEphemeralStorage: resource.MustParse("50Gi"),
					},
					Capacity: corev1.ResourceList{
						corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
					},
				},
			}

			infos, zones := clusterinfo.BuildNodeInfoList([]corev1.Node{node})

			Expect(infos).To(HaveLen(1))
			Expect(infos[0].Name).To(Equal("worker-1"))
			Expect(infos[0].Ready).To(BeTrue())
			Expect(infos[0].AllocatableCPU).To(Equal("3800m"))
			Expect(infos[0].AllocatableMemory).To(Equal("7Gi"))
			Expect(infos[0].AllocatableEphemeralDisk).To(Equal("50Gi"))
			Expect(infos[0].CapacityEphemeralDisk).To(Equal("100Gi"))
			Expect(infos[0].Topology.Zone).To(Equal("us-east-1a"))
			Expect(infos[0].Topology.Region).To(Equal("us-east-1"))
			Expect(zones).To(ConsistOf("us-east-1a"))
		})

		It("falls back to legacy failure-domain labels when topology labels absent", func() {
			node := corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-2",
					Labels: map[string]string{
						"failure-domain.beta.kubernetes.io/zone":   "eu-west-1b",
						"failure-domain.beta.kubernetes.io/region": "eu-west-1",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:              resource.MustParse("2000m"),
						corev1.ResourceMemory:           resource.MustParse("4Gi"),
						corev1.ResourceEphemeralStorage: resource.MustParse("20Gi"),
					},
					Capacity: corev1.ResourceList{
						corev1.ResourceEphemeralStorage: resource.MustParse("50Gi"),
					},
				},
			}

			infos, zones := clusterinfo.BuildNodeInfoList([]corev1.Node{node})

			Expect(infos[0].Topology.Zone).To(Equal("eu-west-1b"))
			Expect(infos[0].Topology.Region).To(Equal("eu-west-1"))
			Expect(zones).To(ConsistOf("eu-west-1b"))
		})

		It("deduplicates availability zones", func() {
			nodes := []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "worker-a",
						Labels: map[string]string{"topology.kubernetes.io/zone": "us-east-1a"},
					},
					Status: corev1.NodeStatus{
						Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
						Allocatable: corev1.ResourceList{},
						Capacity:    corev1.ResourceList{},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "worker-b",
						Labels: map[string]string{"topology.kubernetes.io/zone": "us-east-1a"},
					},
					Status: corev1.NodeStatus{
						Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
						Allocatable: corev1.ResourceList{},
						Capacity:    corev1.ResourceList{},
					},
				},
			}

			_, zones := clusterinfo.BuildNodeInfoList(nodes)
			Expect(zones).To(HaveLen(1))
			Expect(zones).To(ConsistOf("us-east-1a"))
		})
	})

	Describe("BuildStorageClassInfoList", func() {
		It("marks the default storage class", func() {
			scs := []storagev1.StorageClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "fast-ssd",
						Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"},
					},
					Provisioner: "kubernetes.io/aws-ebs",
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "slow-hdd"},
					Provisioner: "kubernetes.io/gce-pd",
				},
			}

			infos := clusterinfo.BuildStorageClassInfoList(scs)

			Expect(infos).To(HaveLen(2))
			Expect(infos[0].Name).To(Equal("fast-ssd"))
			Expect(infos[0].IsDefault).To(BeTrue())
			Expect(infos[0].Provisioner).To(Equal("kubernetes.io/aws-ebs"))
			Expect(infos[1].IsDefault).To(BeFalse())
		})
	})

	Describe("BuildLoadBalancerInfoList", func() {
		It("extracts IPs from LoadBalancer services only", func() {
			svcs := []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
					Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
					Status: corev1.ServiceStatus{
						LoadBalancer: corev1.LoadBalancerStatus{
							Ingress: []corev1.LoadBalancerIngress{{IP: "192.168.1.100"}},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "internal-svc", Namespace: "default"},
					Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP},
				},
			}

			infos := clusterinfo.BuildLoadBalancerInfoList(svcs)

			Expect(infos).To(HaveLen(1))
			Expect(infos[0].ServiceName).To(Equal("traefik"))
			Expect(infos[0].ServiceNamespace).To(Equal("traefik"))
			Expect(infos[0].IngressIPs).To(ConsistOf("192.168.1.100"))
			Expect(infos[0].HasIP).To(BeTrue())
		})

		It("sets HasIP to false when no IP assigned yet", func() {
			svcs := []corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pending-lb", Namespace: "default"},
					Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
					Status:     corev1.ServiceStatus{},
				},
			}

			infos := clusterinfo.BuildLoadBalancerInfoList(svcs)
			Expect(infos[0].HasIP).To(BeFalse())
			Expect(infos[0].IngressIPs).To(BeEmpty())
		})
	})

	Describe("BuildIngressClassInfoList", func() {
		It("marks the default ingress class", func() {
			ics := []networkv1.IngressClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "nginx",
						Annotations: map[string]string{"ingressclass.kubernetes.io/is-default-class": "true"},
					},
					Spec: networkv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"},
				},
			}

			infos := clusterinfo.BuildIngressClassInfoList(ics)

			Expect(infos[0].IsDefault).To(BeTrue())
			Expect(infos[0].Controller).To(Equal("k8s.io/ingress-nginx"))
		})
	})
})
