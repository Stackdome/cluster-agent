package integration

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/bootstrap"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

// Verifies the ImageBuild cleanup finalizer: when an ImageBuild that pushed to
// an internal ClusterRegistry is deleted, its image is removed from the
// registry. Exercises the full path — real build+push, Stack teardown cascading
// to the ImageBuild, finalizer authenticating with the ClusterRegistry's own
// credentials, and a real manifest delete — then asserts the tag is gone by
// querying the registry over a port-forward.
var _ = Describe("ImageBuild registry cleanup", Ordered, func() {
	const (
		regName  = "finalizer-test-registry"
		regCreds = "finalizer-test-registry-creds"
		regPort  = int32(5002)
		regUser  = "finuser"
		regPass  = "finpass"
	)
	var (
		stack      *corev1alpha1.Stack
		buildKey   client.ObjectKey
		repoAndTag string
	)

	// tagPresent polls the registry for the built image. Transient errors are
	// swallowed by the callers' Eventually so port-forward hiccups don't fail.
	tagPresent := func() (bool, error) {
		return helpers.ImageTagPresent(ctx, env.RestConfig, env.KubeClient,
			bootstrap.RegistryNamespace, regName, int(regPort), repoAndTag, regUser, regPass)
	}

	BeforeAll(func() {
		By("Creating registry credentials in the registry namespace")
		Expect(c.Create(ctx, fixtures.ClusterRegistryCredentialsSecret(
			regCreds, bootstrap.RegistryNamespace, regUser, regPass))).To(Succeed())

		By("Creating the ClusterRegistry and waiting for it to be Running")
		Expect(c.Create(ctx, fixtures.SimpleClusterRegistry(regName, regCreds, regPort))).To(Succeed())
		_, err := helpers.WaitForClusterRegistryReady(ctx, c, client.ObjectKey{Name: regName}, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Creating build credentials in the test namespace")
		Expect(c.Create(ctx, fixtures.ClusterRegistryCredentialsSecret(
			regCreds, env.TestNamespace, regUser, regPass))).To(Succeed())

		By("Creating a Stack that builds and pushes via ClusterRegistryRef")
		swr := fixtures.BuildStackWithClusterRegistry("finalizer-build", regName, regCreds, env.TestNamespace)
		Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
		stack = swr.Stack
	})

	It("builds and pushes the image to the registry", func() {
		srName := stack.Spec.ResourceNames[0]

		ib, err := helpers.WaitForImageBuildCreated(ctx, c, env.TestNamespace, srName, imageBuildTimeout)
		Expect(err).NotTo(HaveOccurred())
		buildKey = client.ObjectKeyFromObject(ib)

		completed, err := helpers.WaitForImageBuildComplete(ctx, c, buildKey, buildReadyTimeout)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed.Status.ImageUrl).NotTo(BeEmpty())

		// Strip the registry host, leaving "<repository>:<tag>".
		_, repoAndTag, _ = strings.Cut(completed.Status.ImageUrl, "/")
		Expect(repoAndTag).To(ContainSubstring(":"), "ImageUrl %q should contain a tag", completed.Status.ImageUrl)
	})

	It("has the pushed image present in the registry", func() {
		Eventually(func() bool {
			present, err := tagPresent()
			if err != nil {
				return false // keep polling through transient port-forward errors
			}
			return present
		}, 30*time.Second, 3*time.Second).Should(BeTrue(),
			"image %q should be present after a successful build", repoAndTag)
	})

	It("removes the image from the registry when the ImageBuild is deleted", func() {
		By("Deleting the Stack (cascades StackResource -> ImageBuild)")
		helpers.CleanupStack(ctx, c, stack)
		stack = nil

		By("Waiting for the ImageBuild to be fully deleted (cleanup finalizer cleared)")
		Eventually(func() bool {
			return apierrors.IsNotFound(c.Get(ctx, buildKey, &buildsv1alpha1.ImageBuild{}))
		}, imageBuildTimeout, 3*time.Second).Should(BeTrue(),
			"ImageBuild should be deleted once its cleanup finalizer completes")

		By("Verifying the image tag is gone from the registry")
		Eventually(func() bool {
			present, err := tagPresent()
			if err != nil {
				return true // keep polling until it is confirmably absent
			}
			return present
		}, time.Minute, 3*time.Second).Should(BeFalse(),
			"image %q should be removed from the registry when its ImageBuild is deleted", repoAndTag)
	})

	AfterAll(func() {
		if stack != nil {
			helpers.CleanupStack(ctx, c, stack)
		}
		reg := &registryv1alpha1.ClusterRegistry{}
		reg.Name = regName
		_ = c.Delete(ctx, reg)
		_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: regCreds, Namespace: bootstrap.RegistryNamespace}})
		_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: regCreds, Namespace: env.TestNamespace}})
	})
})
