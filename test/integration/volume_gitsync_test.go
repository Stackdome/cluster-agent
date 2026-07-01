package integration

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

var _ = Describe("Volume git-sync with pinned commit", Ordered, func() {
	const volumeName = "gitsync-test-vol"

	var volume *storagev1alpha1.Volume

	BeforeAll(func() {
		volume = &storagev1alpha1.Volume{
			ObjectMeta: metav1.ObjectMeta{
				Name:      volumeName,
				Namespace: env.TestNamespace,
			},
			Spec: storagev1alpha1.VolumeSpec{
				Size:               "1Gi",
				AccessMode:         corev1.ReadWriteOnce,
				NeedsSyncBeforeUse: true,
				Source: &storagev1alpha1.VolumeSource{
					GitRepo: &storagev1alpha1.GitRepoSource{
						RepoUrl: fixtures.PublicTestRepoURL,
						Revision: corev1alpha1.GitRepoRevision{
							Branch: fixtures.BuildSourceBranch,
							Commit: fixtures.BuildSourceCommit,
						},
						DestinationWithinVolume: "repo",
					},
				},
			},
		}
		Expect(c.Create(ctx, volume)).To(Succeed())
	})

	It("should create a git-sync Job with --ref pointing to the commit", func() {
		var matchingJob *batchv1.Job
		Eventually(func() bool {
			var jobList batchv1.JobList
			if err := c.List(ctx, &jobList, client.InNamespace(env.TestNamespace)); err != nil {
				return false
			}
			for i := range jobList.Items {
				for _, ref := range jobList.Items[i].OwnerReferences {
					if ref.Name == volumeName {
						matchingJob = &jobList.Items[i]
						return true
					}
				}
			}
			return false
		}, 2*time.Minute, "5s").Should(BeTrue())

		args := matchingJob.Spec.Template.Spec.Containers[0].Args
		joined := strings.Join(args, " ")
		Expect(joined).To(ContainSubstring("--ref=" + fixtures.BuildSourceCommit))
	})

	It("should complete the sync successfully", func() {
		Eventually(func() bool {
			v := &storagev1alpha1.Volume{}
			if err := c.Get(ctx, client.ObjectKey{Name: volumeName, Namespace: env.TestNamespace}, v); err != nil {
				return false
			}
			cond := meta.FindStatusCondition(v.Status.Conditions, string(storagev1alpha1.VolumeConditionSyncedFromGitSource))
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, readyTimeout, "5s").Should(BeTrue())
	})

	It("should record the synced commit in status", func() {
		v := &storagev1alpha1.Volume{}
		Expect(c.Get(ctx, client.ObjectKey{Name: volumeName, Namespace: env.TestNamespace}, v)).To(Succeed())
		Expect(v.Status.LastSyncedGitReference).To(Equal(fixtures.BuildSourceCommit))
		Expect(v.Status.GitRepoSyncedPathWithinVolume).To(Equal("repo"))
	})

	It("should build an image from the synced volume", func() {
		By("Creating a Stack with a volume-sourced build")
		stackName := "vol-build-test"
		resourceName := stackName + "-build"
		swr := &fixtures.StackWithResources{
			Stack: &corev1alpha1.Stack{
				ObjectMeta: metav1.ObjectMeta{
					Name:      stackName,
					Namespace: env.TestNamespace,
				},
				Spec: corev1alpha1.StackSpec{
					ResourceNames: []string{resourceName},
				},
			},
			Resources: []*corev1alpha1.StackResource{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: env.TestNamespace,
					},
					Spec: corev1alpha1.StackResourceSpec{
						BuildSpec: &corev1alpha1.StackResourceBuildSpec{
							SourceContext: corev1alpha1.BuildContextSource{
								Volume: &corev1alpha1.VolumeSource{Name: volumeName},
							},
							BuildContext:   ".",
							DockerFilePath: "Dockerfile",
							SourceRevision: corev1alpha1.SourceRevisionSpec{
								Volume: &corev1alpha1.VolumeRevision{
									RevisionString: fixtures.BuildSourceCommit,
								},
							},
							Repository: fixtures.RepositorySpecWithAuth(env.RegistryURL),
						},
						Ports: []corev1alpha1.Port{
							{Name: "http", Number: 3000, Protocol: "http"},
						},
					},
				},
			},
		}
		Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())

		By("Waiting for ImageBuild to complete")
		imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, resourceName, imageBuildTimeout)
		Expect(err).NotTo(HaveOccurred())
		completedBuild, err := helpers.WaitForImageBuildComplete(ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying the image was pushed to the registry")
		Expect(completedBuild.Status.ImageUrl).To(ContainSubstring(env.RegistryURL))

		By("Waiting for StackResource to become Available")
		_, err = helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
			Name:      resourceName,
			Namespace: env.TestNamespace,
		}, readyTimeout)
		Expect(err).NotTo(HaveOccurred())

		helpers.CleanupStack(ctx, c, swr.Stack)
	})

	AfterAll(func() {
		if volume != nil {
			_ = c.Delete(ctx, volume)
		}
	})
})
