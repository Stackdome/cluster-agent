package stackresource

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/mocks"
)

func newBuildTestResource(sourceRevision string) *v1alpha1.StackResource {
	return &v1alpha1.StackResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-resource",
			Namespace:  "test-ns",
			Generation: 1,
			UID:        "test-uid",
		},
		Spec: v1alpha1.StackResourceSpec{
			BuildSpec: &v1alpha1.StackResourceBuildSpec{
				SourceContext: v1alpha1.BuildContextSource{
					Volume: &v1alpha1.VolumeSource{Name: "src-vol"},
				},
				BuildContext:   "/",
				DockerFilePath: "Dockerfile",
				SourceRevision: v1alpha1.SourceRevisionSpec{
					Volume: &v1alpha1.VolumeRevision{
						CurrentVolumeHash: sourceRevision,
					},
				},
				Registry: v1alpha1.RegistrySpec{
					RepositoryURL: "registry.local/test",
				},
			},
		},
	}
}

func buildCondition(condType buildsv1alpha1.BuildStatusCondition, status metav1.ConditionStatus) metav1.Condition {
	return metav1.Condition{
		Type:   string(condType),
		Status: status,
	}
}

func inProgressBuildStatus() buildsv1alpha1.ImageBuildStatus {
	return buildsv1alpha1.ImageBuildStatus{
		Conditions: []metav1.Condition{
			buildCondition(buildsv1alpha1.BuildJobCreated, metav1.ConditionTrue),
		},
	}
}

func successBuildStatus() buildsv1alpha1.ImageBuildStatus {
	return buildsv1alpha1.ImageBuildStatus{
		Conditions: []metav1.Condition{
			buildCondition(buildsv1alpha1.BuildAvailable, metav1.ConditionTrue),
		},
	}
}

func failedBuildStatus() buildsv1alpha1.ImageBuildStatus {
	return buildsv1alpha1.ImageBuildStatus{
		Conditions: []metav1.Condition{
			buildCondition(buildsv1alpha1.BuildFailed, metav1.ConditionTrue),
		},
	}
}

func cancelledBuildStatus() buildsv1alpha1.ImageBuildStatus {
	return buildsv1alpha1.ImageBuildStatus{
		Conditions: []metav1.Condition{
			buildCondition(buildsv1alpha1.BuildCancelled, metav1.ConditionTrue),
		},
	}
}

var _ = Describe("cancelStaleImageBuilds", func() {
	var (
		mockCtrl   *gomock.Controller
		mockClient *mocks.MockClient
		reconciler *imageBuildReconciler
		resource   *v1alpha1.StackResource
		ctx        context.Context
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mocks.NewMockClient(mockCtrl)
		ctx = context.Background()

		scheme := runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(buildsv1alpha1.AddToScheme(scheme)).To(Succeed())

		reconciler = &imageBuildReconciler{
			Client:       mockClient,
			scheme:       scheme,
			historyLimit: 5,
		}

		resource = newBuildTestResource("new-sha")
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	Context("when there are stale ImageBuilds from previous revisions", func() {
		It("cancels stale builds by setting Spec.Cancelled=true", func() {
			currentBuildName := buildsv1alpha1.ImageBuildName("test-resource", "new-sha")
			oldBuildName := buildsv1alpha1.ImageBuildName("test-resource", "old-sha")

			builds := []buildsv1alpha1.ImageBuild{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:            currentBuildName,
						Namespace:       "test-ns",
						OwnerReferences: []metav1.OwnerReference{{UID: resource.UID}},
					},
					Status: inProgressBuildStatus(),
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:            oldBuildName,
						Namespace:       "test-ns",
						OwnerReferences: []metav1.OwnerReference{{UID: resource.UID}},
					},
					Status: inProgressBuildStatus(),
				},
			}

			var updatedBuild *buildsv1alpha1.ImageBuild
			mockClient.EXPECT().
				Update(gomock.Any(), gomock.AssignableToTypeOf(&buildsv1alpha1.ImageBuild{})).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
					updatedBuild = obj.(*buildsv1alpha1.ImageBuild)
					return nil
				})

			err := reconciler.cancelStaleImageBuilds(ctx, resource, builds)

			Expect(err).NotTo(HaveOccurred())
			Expect(updatedBuild).NotTo(BeNil())
			Expect(updatedBuild.Name).To(Equal(oldBuildName))
			Expect(updatedBuild.Spec.Cancelled).To(BeTrue())
		})

		It("does not cancel the current build", func() {
			currentBuildName := buildsv1alpha1.ImageBuildName("test-resource", "new-sha")

			builds := []buildsv1alpha1.ImageBuild{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:            currentBuildName,
						Namespace:       "test-ns",
						OwnerReferences: []metav1.OwnerReference{{UID: resource.UID}},
					},
					Status: inProgressBuildStatus(),
				},
			}

			err := reconciler.cancelStaleImageBuilds(ctx, resource, builds)

			Expect(err).NotTo(HaveOccurred())
		})

		It("skips builds not owned by this StackResource", func() {
			oldBuildName := buildsv1alpha1.ImageBuildName("test-resource", "old-sha")

			builds := []buildsv1alpha1.ImageBuild{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:            oldBuildName,
						Namespace:       "test-ns",
						OwnerReferences: []metav1.OwnerReference{{UID: types.UID("different-uid")}},
					},
					Status: inProgressBuildStatus(),
				},
			}

			err := reconciler.cancelStaleImageBuilds(ctx, resource, builds)

			Expect(err).NotTo(HaveOccurred())
		})

		It("skips builds already cancelled", func() {
			oldBuildName := buildsv1alpha1.ImageBuildName("test-resource", "old-sha")

			builds := []buildsv1alpha1.ImageBuild{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:            oldBuildName,
						Namespace:       "test-ns",
						OwnerReferences: []metav1.OwnerReference{{UID: resource.UID}},
					},
					Spec:   buildsv1alpha1.ImageBuildSpec{Cancelled: true},
					Status: cancelledBuildStatus(),
				},
			}

			err := reconciler.cancelStaleImageBuilds(ctx, resource, builds)

			Expect(err).NotTo(HaveOccurred())
		})

		It("does not cancel terminal builds", func() {
			oldBuildName := buildsv1alpha1.ImageBuildName("test-resource", "old-sha")

			builds := []buildsv1alpha1.ImageBuild{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:            oldBuildName,
						Namespace:       "test-ns",
						OwnerReferences: []metav1.OwnerReference{{UID: resource.UID}},
					},
					Status: successBuildStatus(),
				},
			}

			err := reconciler.cancelStaleImageBuilds(ctx, resource, builds)

			Expect(err).NotTo(HaveOccurred())
		})
	})
})

var _ = Describe("enforceImageBuildRetention", func() {
	var (
		mockCtrl   *gomock.Controller
		mockClient *mocks.MockClient
		reconciler *imageBuildReconciler
		resource   *v1alpha1.StackResource
		ctx        context.Context
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mocks.NewMockClient(mockCtrl)
		ctx = context.Background()

		resource = newBuildTestResource("current-sha")
		reconciler = &imageBuildReconciler{
			Client:       mockClient,
			historyLimit: 2,
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	It("deletes terminal builds beyond the history limit", func() {
		now := time.Now()
		buildNames := []string{
			buildsv1alpha1.ImageBuildName("test-resource", "rev-newest"),
			buildsv1alpha1.ImageBuildName("test-resource", "rev-middle"),
			buildsv1alpha1.ImageBuildName("test-resource", "rev-oldest"),
		}

		builds := []buildsv1alpha1.ImageBuild{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              buildNames[0],
					Namespace:         "test-ns",
					CreationTimestamp: metav1.NewTime(now),
					OwnerReferences:   []metav1.OwnerReference{{UID: resource.UID}},
				},
				Status: cancelledBuildStatus(),
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              buildNames[1],
					Namespace:         "test-ns",
					CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
					OwnerReferences:   []metav1.OwnerReference{{UID: resource.UID}},
				},
				Status: successBuildStatus(),
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              buildNames[2],
					Namespace:         "test-ns",
					CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
					OwnerReferences:   []metav1.OwnerReference{{UID: resource.UID}},
				},
				Status: failedBuildStatus(),
			},
		}

		var deletedBuild client.Object
		mockClient.EXPECT().
			Delete(gomock.Any(), gomock.AssignableToTypeOf(&buildsv1alpha1.ImageBuild{}), gomock.Any()).
			DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
				deletedBuild = obj
				return nil
			})

		err := reconciler.enforceImageBuildRetention(ctx, resource, builds)

		Expect(err).NotTo(HaveOccurred())
		Expect(deletedBuild).NotTo(BeNil())
		Expect(deletedBuild.GetName()).To(Equal(buildNames[2]))
	})

	It("does not delete non-terminal builds beyond the limit", func() {
		now := time.Now()

		builds := []buildsv1alpha1.ImageBuild{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              buildsv1alpha1.ImageBuildName("test-resource", "rev-a"),
					Namespace:         "test-ns",
					CreationTimestamp: metav1.NewTime(now),
					OwnerReferences:   []metav1.OwnerReference{{UID: resource.UID}},
				},
				Status: successBuildStatus(),
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              buildsv1alpha1.ImageBuildName("test-resource", "rev-b"),
					Namespace:         "test-ns",
					CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
					OwnerReferences:   []metav1.OwnerReference{{UID: resource.UID}},
				},
				Status: cancelledBuildStatus(),
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              buildsv1alpha1.ImageBuildName("test-resource", "rev-c"),
					Namespace:         "test-ns",
					CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
					OwnerReferences:   []metav1.OwnerReference{{UID: resource.UID}},
				},
				Status: inProgressBuildStatus(),
			},
		}

		err := reconciler.enforceImageBuildRetention(ctx, resource, builds)

		Expect(err).NotTo(HaveOccurred())
	})

	It("does not delete builds not owned by this StackResource", func() {
		now := time.Now()

		builds := []buildsv1alpha1.ImageBuild{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              buildsv1alpha1.ImageBuildName("test-resource", "rev-a"),
					Namespace:         "test-ns",
					CreationTimestamp: metav1.NewTime(now),
					OwnerReferences:   []metav1.OwnerReference{{UID: resource.UID}},
				},
				Status: successBuildStatus(),
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              buildsv1alpha1.ImageBuildName("test-resource", "rev-b"),
					Namespace:         "test-ns",
					CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
					OwnerReferences:   []metav1.OwnerReference{{UID: resource.UID}},
				},
				Status: cancelledBuildStatus(),
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:              buildsv1alpha1.ImageBuildName("test-resource", "rev-c"),
					Namespace:         "test-ns",
					CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
					OwnerReferences:   []metav1.OwnerReference{{UID: types.UID("different-uid")}},
				},
				Status: failedBuildStatus(),
			},
		}

		err := reconciler.enforceImageBuildRetention(ctx, resource, builds)

		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("imageBuildReconciler.reconcile - re-used source revision", func() {
	var (
		mockCtrl   *gomock.Controller
		mockClient *mocks.MockClient
		reconciler *imageBuildReconciler
		resource   *v1alpha1.StackResource
		ctx        context.Context
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mocks.NewMockClient(mockCtrl)
		ctx = context.Background()

		scheme := runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(buildsv1alpha1.AddToScheme(scheme)).To(Succeed())

		reconciler = &imageBuildReconciler{
			Client:       mockClient,
			scheme:       scheme,
			historyLimit: 5,
		}

		resource = newBuildTestResource("reused-sha")
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	Context("when the current ImageBuild has Spec.Cancelled=true", func() {
		It("deletes the cancelled ImageBuild and creates a fresh one", func() {
			imageBuildName := buildsv1alpha1.ImageBuildName("test-resource", "reused-sha")

			// listImageBuilds returns empty list
			mockClient.EXPECT().
				List(gomock.Any(), gomock.AssignableToTypeOf(&buildsv1alpha1.ImageBuildList{}), gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					list.(*buildsv1alpha1.ImageBuildList).Items = []buildsv1alpha1.ImageBuild{}
					return nil
				})

			// Get returns the existing ImageBuild with Cancelled=true
			mockClient.EXPECT().
				Get(gomock.Any(), client.ObjectKey{Name: imageBuildName, Namespace: "test-ns"}, gomock.AssignableToTypeOf(&buildsv1alpha1.ImageBuild{})).
				DoAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					build := obj.(*buildsv1alpha1.ImageBuild)
					build.Name = imageBuildName
					build.Namespace = "test-ns"
					build.Spec.Cancelled = true
					build.Status = cancelledBuildStatus()
					return nil
				})

			// Delete the cancelled build
			mockClient.EXPECT().
				Delete(gomock.Any(), gomock.AssignableToTypeOf(&buildsv1alpha1.ImageBuild{}), gomock.Any()).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
					Expect(obj.GetName()).To(Equal(imageBuildName))
					return nil
				})

			// Create the fresh ImageBuild
			var createdBuild *buildsv1alpha1.ImageBuild
			mockClient.EXPECT().
				Create(gomock.Any(), gomock.AssignableToTypeOf(&buildsv1alpha1.ImageBuild{}), gomock.Any()).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
					createdBuild = obj.(*buildsv1alpha1.ImageBuild)
					return nil
				})

			result, err := reconciler.reconcile(ctx, resource)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(resultStop))
			Expect(createdBuild).NotTo(BeNil())
			Expect(createdBuild.Name).To(Equal(imageBuildName))
			Expect(createdBuild.Spec.Cancelled).To(BeFalse())
			Expect(createdBuild.Spec.SourceRevision.GetSourceRevisionString()).To(Equal("reused-sha"))
		})
	})
})
