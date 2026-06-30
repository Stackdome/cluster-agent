package imagebuild

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	batchv1 "k8s.io/api/batch/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/mocks"
)

var _ = Describe("reconcileCancellation", func() {
	var (
		mockCtrl   *gomock.Controller
		mockClient *mocks.MockClient
		reconciler *ImageBuildReconciler
		ctx        context.Context
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mocks.NewMockClient(mockCtrl)
		ctx = context.Background()

		scheme := runtime.NewScheme()
		Expect(buildsv1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())

		reconciler = &ImageBuildReconciler{
			Client: mockClient,
			Scheme: scheme,
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	Context("when ImageBuild has Spec.Cancelled=true", Ordered, func() {
		var (
			buildConfig *buildsv1alpha1.ImageBuild
			deletedJob  client.Object
		)

		BeforeAll(func() {
			mockCtrl = gomock.NewController(GinkgoT())
			mockClient = mocks.NewMockClient(mockCtrl)

			scheme := runtime.NewScheme()
			Expect(buildsv1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())

			reconciler = &ImageBuildReconciler{
				Client: mockClient,
				Scheme: scheme,
			}
			ctx = context.Background()

			buildConfig = &buildsv1alpha1.ImageBuild{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-build",
					Namespace: "test-ns",
					UID:       types.UID("build-uid"),
				},
				Spec: buildsv1alpha1.ImageBuildSpec{
					Cancelled:    true,
					ResourceName: "test-resource",
					SourceRevision: corev1alpha1.SourceRevisionSpec{
						Volume: &corev1alpha1.VolumeRevision{
							RevisionString: "test-rev",
						},
					},
				},
			}

			job := batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-build-job",
					Namespace: "test-ns",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "builds.stackdome.io/v1alpha1",
							Kind:       "ImageBuild",
							Name:       "test-build",
							UID:        "build-uid",
							Controller: ptr.To(true),
						},
					},
				},
			}

			mockClient.EXPECT().
				List(gomock.Any(), gomock.AssignableToTypeOf(&batchv1.JobList{}), gomock.Any()).
				DoAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					list.(*batchv1.JobList).Items = []batchv1.Job{job}
					return nil
				})

			mockClient.EXPECT().
				Delete(gomock.Any(), gomock.AssignableToTypeOf(&batchv1.Job{}), gomock.Any()).
				DoAndReturn(func(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
					deletedJob = obj
					return nil
				})

			result, err := reconciler.reconcileCancellation(ctx, buildConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse())
		})

		It("deletes the owned build Job", func() {
			Expect(deletedJob).NotTo(BeNil())
			Expect(deletedJob.GetName()).To(Equal("test-build-job"))
			Expect(deletedJob.GetNamespace()).To(Equal("test-ns"))
		})

		It("sets Phase to Cancelled", func() {
			Expect(buildConfig.Status.Phase).To(Equal(buildsv1alpha1.BuildPhaseCancelled))
		})

		It("sets BuildCancelled condition to True", func() {
			cond := apimeta.FindStatusCondition(buildConfig.Status.Conditions, string(buildsv1alpha1.BuildCancelled))
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("BuildCancelled"))
		})
	})
})

var _ = Describe("validateGitRevision", func() {
	It("accepts branch + commit", func() {
		rev := &corev1alpha1.GitRepoRevision{
			Branch: "main",
			Commit: "abc123",
		}
		Expect(validateGitRevision(rev)).To(Succeed())
	})

	It("accepts tag + commit", func() {
		rev := &corev1alpha1.GitRepoRevision{
			Tag:    "v1.0.0",
			Commit: "abc123",
		}
		Expect(validateGitRevision(rev)).To(Succeed())
	})

	It("rejects nil revision", func() {
		Expect(validateGitRevision(nil)).To(HaveOccurred())
	})

	It("rejects empty commit", func() {
		rev := &corev1alpha1.GitRepoRevision{
			Branch: "main",
		}
		Expect(validateGitRevision(rev)).To(HaveOccurred())
	})

	It("rejects HEAD placeholder", func() {
		rev := &corev1alpha1.GitRepoRevision{
			Branch: "main",
			Commit: "HEAD",
		}
		Expect(validateGitRevision(rev)).To(HaveOccurred())
	})

	It("rejects commit without fetchable ref", func() {
		rev := &corev1alpha1.GitRepoRevision{
			Commit: "abc123",
		}
		Expect(validateGitRevision(rev)).To(HaveOccurred())
	})
})
