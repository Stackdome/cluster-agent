package workload

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/mocks"
)

var _ = Describe("capturePodFailureDetails (selector-scoped)", func() {
	var (
		mockCtrl     *gomock.Controller
		mockUncached *mocks.MockClient
		ctx          context.Context
		resource     *v1alpha1.StackResource
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockUncached = mocks.NewMockClient(mockCtrl)
		ctx = context.Background()
		resource = &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-resource",
				Namespace: "test-ns",
			},
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	It("should find crashing pod by resource label and extract failure details", func() {
		pod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-resource-abc123",
				Namespace:         "test-ns",
				CreationTimestamp: metav1.Now(),
				Labels:            map[string]string{"resource": "test-resource"},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:         "app",
						RestartCount: 3,
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason: "CrashLoopBackOff",
							},
						},
						LastTerminationState: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 1,
								Reason:   "Error",
								Message:  "connection refused",
							},
						},
					},
				},
			},
		}

		mockUncached.EXPECT().
			List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.PodList{}), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				list.(*corev1.PodList).Items = []corev1.Pod{pod}
				return nil
			})

		details, err := capturePodFailureDetails(ctx, mockUncached, resource)

		Expect(err).NotTo(HaveOccurred())
		Expect(details).To(HaveLen(1))
		Expect(details[0].ContainerName).To(Equal("app"))
		Expect(details[0].LastTerminationExitCode).To(Equal(ptr.To(int32(1))))
		Expect(details[0].LastTerminationReason).To(Equal("Error"))
		Expect(details[0].LastTerminationMessage).To(Equal("connection refused"))
		Expect(details[0].RestartCount).To(Equal(int32(3)))
	})

	It("should pick the newest pod when multiple pods match", func() {
		olderPod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pod-old",
				Namespace:         "test-ns",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
				Labels:            map[string]string{"resource": "test-resource"},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:  "app",
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					},
				},
			},
		}
		newerPod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pod-new",
				Namespace:         "test-ns",
				CreationTimestamp: metav1.Now(),
				Labels:            map[string]string{"resource": "test-resource"},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:         "app",
						RestartCount: 2,
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason: "CrashLoopBackOff",
							},
						},
						LastTerminationState: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 2,
								Reason:   "Error",
							},
						},
					},
				},
			},
		}

		mockUncached.EXPECT().
			List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.PodList{}), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				list.(*corev1.PodList).Items = []corev1.Pod{olderPod, newerPod}
				return nil
			})

		details, err := capturePodFailureDetails(ctx, mockUncached, resource)

		Expect(err).NotTo(HaveOccurred())
		Expect(details).To(HaveLen(1))
		Expect(details[0].LastTerminationExitCode).To(Equal(ptr.To(int32(2))))
	})

	It("should return nil when no pods are crashing", func() {
		healthyPod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pod-healthy",
				Namespace:         "test-ns",
				CreationTimestamp: metav1.Now(),
				Labels:            map[string]string{"resource": "test-resource"},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:  "app",
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					},
				},
			},
		}

		mockUncached.EXPECT().
			List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.PodList{}), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				list.(*corev1.PodList).Items = []corev1.Pod{healthyPod}
				return nil
			})

		details, err := capturePodFailureDetails(ctx, mockUncached, resource)

		Expect(err).NotTo(HaveOccurred())
		Expect(details).To(BeNil())
	})

	It("should return nil when no pods match", func() {
		mockUncached.EXPECT().
			List(gomock.Any(), gomock.AssignableToTypeOf(&corev1.PodList{}), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				list.(*corev1.PodList).Items = []corev1.Pod{}
				return nil
			})

		details, err := capturePodFailureDetails(ctx, mockUncached, resource)

		Expect(err).NotTo(HaveOccurred())
		Expect(details).To(BeNil())
	})
})

var _ = Describe("capturePodFailureDetailsOnce (dedup)", func() {
	var (
		mockCtrl     *gomock.Controller
		mockUncached *mocks.MockClient
		ctx          context.Context
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockUncached = mocks.NewMockClient(mockCtrl)
		ctx = context.Background()
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	It("should skip capture when failureKey matches LastFailureDeploymentRevision", func() {
		resource := &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-resource",
				Namespace: "test-ns",
			},
			Status: v1alpha1.StackResourceStatus{
				LastFailureDeploymentRevision: "rev-1",
				LastFailureDetails: []v1alpha1.LastFailureDetail{
					{ContainerName: "existing-failure", RestartCount: 5},
				},
			},
		}

		reconciler := &Reconciler{UncachedClient: mockUncached}

		reconciler.capturePodFailureDetailsOnce(ctx, resource, "rev-1")

		// gomock will fail if List is called — no expectations set
		Expect(resource.Status.LastFailureDetails).To(HaveLen(1))
		Expect(resource.Status.LastFailureDetails[0].ContainerName).To(Equal("existing-failure"))
		Expect(resource.Status.LastFailureDetails[0].RestartCount).To(Equal(int32(5)))
	})
})
