package integration

import (
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

const (
	gitSecretName       = "git-credentials"
	buildArgSecretName  = "build-arg-secret"
	buildArgSecretValue = "test-secret-token-value"
)

var _ = Describe("Stack build from source", func() {

	Context("Build from Source with BuildArgs", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			githubToken := os.Getenv("GITHUB_TOKEN")
			if githubToken == "" {
				Skip("GITHUB_TOKEN not set -- skipping build args tests")
			}

			By("Creating git credentials secret")
			gitSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      gitSecretName,
					Namespace: env.TestNamespace,
				},
				StringData: map[string]string{
					"username": "x-access-token",
					"token":    githubToken,
				},
			}
			Expect(c.Create(ctx, gitSecret)).To(Succeed())

			By("Creating build arg secret")
			argSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      buildArgSecretName,
					Namespace: env.TestNamespace,
				},
				StringData: map[string]string{
					"token": buildArgSecretValue,
				},
			}
			Expect(c.Create(ctx, argSecret)).To(Succeed())
		})

		It("should create a Stack with BuildSpec and BuildArgs", func() {
			swr := fixtures.StackWithBuildArgs(
				"test-build-args",
				env.RegistryURL,
				gitSecretName,
				buildArgSecretName,
			)

			By("Creating the Stack CR")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should create an ImageBuild CR with BuildArgs propagated", func() {
			By("Waiting for ImageBuild to be created")
			imageBuild, err := helpers.WaitForImageBuildCreated(
				ctx, c, env.TestNamespace, fixtures.BuildSourceResourceName, imageBuildTimeout,
			)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying BuildArgs are propagated to the ImageBuild CR")
			Expect(imageBuild.Spec.BuildArgs).To(HaveLen(2))

			var foundInline, foundSecret bool
			for _, arg := range imageBuild.Spec.BuildArgs {
				if arg.Name == "APP_ENV" && arg.Value == "integration-test" {
					foundInline = true
				}
				if arg.Name == "BUILD_TOKEN" && arg.ValueFrom != nil {
					Expect(arg.ValueFrom.SecretKeyRef.Name).To(Equal(buildArgSecretName))
					Expect(arg.ValueFrom.SecretKeyRef.Key).To(Equal("token"))
					foundSecret = true
				}
			}
			Expect(foundInline).To(BeTrue(), "inline build arg APP_ENV should be propagated")
			Expect(foundSecret).To(BeTrue(), "secret-backed build arg BUILD_TOKEN should be propagated")
		})

		It("should create a Kaniko Job with --build-arg flags", func() {
			By("Getting the ImageBuild name")
			imageBuild, err := helpers.WaitForImageBuildCreated(
				ctx, c, env.TestNamespace, fixtures.BuildSourceResourceName, imageBuildTimeout,
			)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the build Job to be created")
			job, err := helpers.WaitForBuildJob(ctx, c, env.TestNamespace, imageBuild.Name, buildJobTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying --build-arg flags on the Kaniko container")
			Expect(helpers.JobHasBuildArg(job, "APP_ENV", "integration-test")).To(BeTrue(),
				"Job should have --build-arg=APP_ENV=integration-test")
			Expect(helpers.JobHasBuildArg(job, "BUILD_TOKEN", buildArgSecretValue)).To(BeTrue(),
				"Job should have --build-arg=BUILD_TOKEN=<resolved-secret-value>")
		})

		It("should complete the build and deploy the image", func() {
			By("Getting the ImageBuild")
			imageBuild, err := helpers.WaitForImageBuildCreated(
				ctx, c, env.TestNamespace, fixtures.BuildSourceResourceName, imageBuildTimeout,
			)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for ImageBuild to complete successfully")
			completedBuild, err := helpers.WaitForImageBuildComplete(
				ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(completedBuild.Status.ImageUrl).NotTo(BeEmpty())

			By("Waiting for StackResource to become Available")
			_, err = helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      fixtures.BuildSourceResourceName,
				Namespace: env.TestNamespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Deployment uses the built image from in-cluster registry")
			deploy, err := helpers.GetDeploymentForResource(ctx, c, env.TestNamespace, fixtures.BuildSourceResourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(deploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring(env.RegistryURL))
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
			_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: gitSecretName, Namespace: env.TestNamespace}})
			_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: buildArgSecretName, Namespace: env.TestNamespace}})
		})
	})

	Context("BuildSpec - ImageBuild naming labels and ownership", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.SimpleBuildStack("naming-build", env.RegistryURL)
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should create an ImageBuild with the correct name prefix", func() {
			srName := stack.Spec.ResourceNames[0]

			By("Waiting for ImageBuild to be created")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ImageBuild name has the StackResource name prefix")
			Expect(strings.HasPrefix(imageBuild.Name, srName+"-")).To(BeTrue(),
				"ImageBuild name %q should have prefix %q", imageBuild.Name, srName+"-")
		})

		It("should have correct labels on the ImageBuild", func() {
			srName := stack.Spec.ResourceNames[0]
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			Expect(imageBuild.Labels).To(HaveKeyWithValue("stackdome.io/component", "image-build"))
			Expect(imageBuild.Labels).To(HaveKeyWithValue("stackdome.io/part-of", srName))
		})

		It("should have owner reference to StackResource", func() {
			srName := stack.Spec.ResourceNames[0]
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			Expect(helpers.HasOwnerReference(imageBuild.ObjectMeta, srName, "StackResource")).To(BeTrue(),
				"ImageBuild should have owner reference to StackResource %s", srName)
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("BuildSpec - simple build without BuildArgs", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.SimpleBuildStack("simple-build", env.RegistryURL)
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should create an ImageBuild with empty BuildArgs", func() {
			srName := stack.Spec.ResourceNames[0]

			By("Waiting for ImageBuild to be created")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying BuildArgs are empty")
			Expect(imageBuild.Spec.BuildArgs).To(BeEmpty())
		})

		It("should complete the build successfully", func() {
			srName := stack.Spec.ResourceNames[0]
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for ImageBuild to complete")
			_, err = helpers.WaitForImageBuildComplete(ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should deploy with the built image from the in-cluster registry", func() {
			srName := stack.Spec.ResourceNames[0]

			By("Waiting for StackResource to become Available")
			_, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: env.TestNamespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Deployment container image references the in-cluster registry")
			dep, err := helpers.GetDeploymentForResource(ctx, c, env.TestNamespace, srName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring(env.RegistryURL))
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("BuildSpec - CurrentBuild status fields", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.SimpleBuildStack("status-build", env.RegistryURL)
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should populate CurrentBuild status after build completes", func() {
			srName := stack.Spec.ResourceNames[0]

			By("Waiting for ImageBuild to complete")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())
			_, err = helpers.WaitForImageBuildComplete(ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for StackResource to become Available")
			sr, err := helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: env.TestNamespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying CurrentBuild status fields")
			Expect(sr.Status.CurrentBuild).NotTo(BeNil(), "CurrentBuild should not be nil")
			Expect(sr.Status.CurrentBuild.Name).NotTo(BeEmpty(), "CurrentBuild.Name should not be empty")
			Expect(sr.Status.CurrentBuild.Phase).To(Equal("Success"), "CurrentBuild.Phase should be Success")
			Expect(sr.Status.CurrentBuild.Available).To(BeTrue(), "CurrentBuild.Available should be true")

			By("Verifying ImageSourceRevision is set")
			Expect(sr.Status.ImageSourceRevision).NotTo(BeEmpty(), "ImageSourceRevision should not be empty")
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("BuildSpec - custom Dockerfile and context paths", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.BuildStackCustomPaths("custpath-build", env.RegistryURL)
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should create an ImageBuild with custom Dockerfile and context paths", func() {
			srName := stack.Spec.ResourceNames[0]

			By("Waiting for ImageBuild to be created")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying custom Dockerfile path")
			Expect(imageBuild.Spec.BuildContext.DockerfilePath).To(Equal("docker/Dockerfile.prod"))

			By("Verifying custom context path")
			Expect(imageBuild.Spec.BuildContext.ContextPath).To(Equal("."))
		})

		It("should complete the build successfully", func() {
			srName := stack.Spec.ResourceNames[0]
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for ImageBuild to complete")
			_, err = helpers.WaitForImageBuildComplete(ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("BuildSpec - source revision update triggers new build", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.SimpleBuildStack("rev-update-build", env.RegistryURL)
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should trigger a new build when source revision is updated", func() {
			srName := stack.Spec.ResourceNames[0]

			By("Waiting for initial ImageBuild to be created")
			firstBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())
			firstBuildName := firstBuild.Name

			By("Waiting for initial build to complete and StackResource to become Available")
			_, err = helpers.WaitForImageBuildComplete(ctx, c, client.ObjectKeyFromObject(firstBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
			_, err = helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: env.TestNamespace,
			}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Updating the source revision on the StackResource to trigger a new build")
			sr := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: srName, Namespace: stack.Namespace}, sr)).To(Succeed())
			sr.Spec.BuildSpec.SourceRevision.GitRepo.Branch.HeadSha = "new-sha"
			Expect(c.Update(ctx, sr)).To(Succeed())

			By("Waiting for a new ImageBuild to be created with a different name")
			Eventually(func() bool {
				list := &buildsv1alpha1.ImageBuildList{}
				if err := c.List(ctx, list, client.InNamespace(env.TestNamespace)); err != nil {
					return false
				}
				for _, ib := range list.Items {
					if strings.HasPrefix(ib.Name, srName+"-") && ib.Name != firstBuildName {
						return true
					}
				}
				return false
			}, imageBuildTimeout, 5*time.Second).Should(BeTrue(),
				"A new ImageBuild with prefix %q and name != %q should be created", srName+"-", firstBuildName)
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("BuildSpec - build failure propagation", Ordered, func() {
		var stack *corev1alpha1.Stack

		BeforeAll(func() {
			swr := fixtures.BuildStackBrokenDockerfile("fail-build", env.RegistryURL)
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should have ImageBuild reach Failed phase", func() {
			srName := stack.Spec.ResourceNames[0]

			By("Waiting for ImageBuild to be created")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for ImageBuild to fail")
			_, err = helpers.WaitForImageBuildFailed(ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should set StackResource to Failed with Stalled=True/BuildFailed", func() {
			srName := stack.Spec.ResourceNames[0]

			Eventually(func() corev1alpha1.StackResourcePhase {
				sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
					Name:      srName,
					Namespace: env.TestNamespace,
				})
				if err != nil {
					return ""
				}
				return sr.Status.Phase
			}, readyTimeout, "5s").Should(Equal(corev1alpha1.StackResourcePhaseFailed))

			sr, err := helpers.GetStackResource(ctx, c, client.ObjectKey{
				Name:      srName,
				Namespace: env.TestNamespace,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(helpers.StackResourceIsAvailable(sr)).To(BeFalse())

			stalledCond := meta.FindStatusCondition(sr.Status.Conditions, string(corev1alpha1.StackResourceStalled))
			Expect(stalledCond).NotTo(BeNil())
			Expect(stalledCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(stalledCond.Reason).To(Equal("BuildFailed"))
		})

		It("should populate BuildFailureDetail with exit status and termination message", func() {
			srName := stack.Spec.ResourceNames[0]

			By("Getting the failed ImageBuild")
			imageBuild, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for BuildFailureDetail to be populated")
			imageBuild, err = helpers.WaitForBuildFailureDetail(ctx, c, client.ObjectKeyFromObject(imageBuild), buildReadyTimeout)
			Expect(err).NotTo(HaveOccurred())

			Expect(imageBuild.Status.LastBuildFailureDetail).NotTo(BeNil())
			detail := imageBuild.Status.LastBuildFailureDetail
			Expect(detail.LastTerminationExitCode).NotTo(BeNil())
			Expect(detail.LastTerminationMessage).NotTo(BeEmpty())
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})

	Context("BuildSpec - source revision update cancels in-progress build", Ordered, func() {
		var stack *corev1alpha1.Stack
		var swr *fixtures.StackWithResources
		var firstBuildName string

		BeforeAll(func() {
			swr = fixtures.SimpleBuildStack("cancel-stale-build", env.RegistryURL)
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack
		})

		It("should cancel the in-progress ImageBuild when source revision is updated", func() {
			srName := stack.Spec.ResourceNames[0]
			sr := swr.Resources[0]
			initialRevision := sr.Spec.BuildSpec.SourceRevision.GetSourceRevisionString()
			firstBuildName = buildsv1alpha1.ImageBuildName(srName, initialRevision)
			firstBuildKey := client.ObjectKey{Name: firstBuildName, Namespace: env.TestNamespace}

			By("Waiting for initial ImageBuild to be created")
			_, err := helpers.WaitForImageBuild(ctx, c, firstBuildKey, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Updating the source revision immediately while the build is still in progress")
			liveSR := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: srName, Namespace: stack.Namespace}, liveSR)).To(Succeed())
			liveSR.Spec.BuildSpec.SourceRevision.GitRepo.Branch.HeadSha = "cancel-test-sha"
			Expect(c.Update(ctx, liveSR)).To(Succeed())

			By("Waiting for the old ImageBuild to have Spec.Cancelled=true")
			Eventually(func() bool {
				old := &buildsv1alpha1.ImageBuild{}
				if err := c.Get(ctx, firstBuildKey, old); err != nil {
					return false
				}
				return old.Spec.Cancelled
			}, imageBuildTimeout, 5*time.Second).Should(BeTrue(),
				"old ImageBuild %q should have Spec.Cancelled=true", firstBuildName)

			By("Verifying a new ImageBuild was created")
			newRevision := liveSR.Spec.BuildSpec.SourceRevision.GetSourceRevisionString()
			newBuildName := buildsv1alpha1.ImageBuildName(srName, newRevision)
			newBuildKey := client.ObjectKey{Name: newBuildName, Namespace: env.TestNamespace}
			_, err = helpers.WaitForImageBuild(ctx, c, newBuildKey, imageBuildTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should delete the old build Job when ImageBuild is cancelled", func() {
			By("Verifying the old build's Job no longer exists")
			Eventually(func() bool {
				_, err := helpers.GetBuildJob(ctx, c, env.TestNamespace, firstBuildName)
				return apierrors.IsNotFound(err)
			}, imageBuildTimeout, 5*time.Second).Should(BeTrue(),
				"build Job for cancelled ImageBuild %q should be deleted", firstBuildName)
		})

		AfterAll(func() { helpers.CleanupStack(ctx, c, stack) })
	})
})
