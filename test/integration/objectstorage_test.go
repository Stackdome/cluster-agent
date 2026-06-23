package integration

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

const (
	osReadyTimeout  = 3 * time.Minute
	osDeleteTimeout = 2 * time.Minute
)

var _ = Describe("ObjectStorage", Ordered, func() {
	var (
		ctx context.Context
		c   client.Client
	)

	BeforeAll(func() {
		ctx = context.Background()
		c = env.Client
	})

	Context("Simple ObjectStorage", func() {
		var os *storagev1alpha1.ObjectStorage

		It("should create a minimal ObjectStorage and reach Available", func() {
			os = fixtures.SimpleObjectStorage("simple-os")

			By("Creating the ObjectStorage CR")
			Expect(c.Create(ctx, os)).To(Succeed())

			By("Waiting for Available condition")
			readyOS, err := helpers.WaitForObjectStorageReady(ctx, c, client.ObjectKeyFromObject(os), osReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyOS.Status.Phase).To(Equal(storagev1alpha1.ObjectStoragePhaseReady))

			By("Verifying status fields are populated")
			Expect(readyOS.Status.Endpoint).NotTo(BeEmpty())
			Expect(readyOS.Status.ExternalEndpoint).NotTo(BeEmpty())
			Expect(readyOS.Status.CredentialsSecretName).NotTo(BeEmpty())
			Expect(readyOS.Status.VolumeName).NotTo(BeEmpty())

			By("Verifying Available condition")
			Expect(helpers.HasObjectStorageCondition(readyOS, storagev1alpha1.ObjectStorageConditionAvailable, metav1.ConditionTrue)).To(BeTrue())
		})

		It("should verify the underlying Volume CR was created", func() {
			vol, err := helpers.GetVolumeForObjectStorage(ctx, c, os.Namespace, os.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(vol.Name).To(Equal(os.VolumeName()))
			Expect(vol.Spec.Size).To(Equal(os.Spec.Capacity))
		})

		It("should verify the credentials Secret was created", func() {
			secret, err := helpers.GetCredentialsSecretForObjectStorage(ctx, c, os.Namespace, os.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Name).To(Equal(os.CredentialsSecretName()))
			Expect(secret.Data).To(HaveKey("access_key_id"))
			Expect(secret.Data).To(HaveKey("secret_access_key"))
		})

		It("should verify the Deployment was created", func() {
			dep, err := helpers.GetDeploymentForObjectStorage(ctx, c, os.Namespace, os.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Name).To(Equal(os.DeploymentName()))
			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring("s3gw"))
		})

		It("should verify the Service was created", func() {
			svc, err := helpers.GetServiceForObjectStorage(ctx, c, os.Namespace, os.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Name).To(Equal(os.ServiceName()))
			Expect(svc.Spec.Ports).NotTo(BeEmpty())
		})

		It("should verify the Ingress was created", func() {
			ingress, err := helpers.GetIngressForObjectStorage(ctx, c, os.Namespace, os.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(ingress.Name).To(Equal(os.IngressName()))
			Expect(ingress.Spec.Rules).To(HaveLen(1))
			Expect(ingress.Spec.Rules[0].Host).To(Equal(os.Spec.Ingress.Hostname))
		})

		AfterAll(func() {
			if os != nil {
				By("Cleaning up simple ObjectStorage")
				_ = c.Delete(ctx, os)
				_ = helpers.WaitForObjectStorageDeleted(ctx, c, client.ObjectKeyFromObject(os), osDeleteTimeout)
			}
		})
	})

	Context("ObjectStorage with buckets", func() {
		var os *storagev1alpha1.ObjectStorage

		It("should create ObjectStorage with bucket specs", func() {
			os = fixtures.ObjectStorageWithBuckets("os-with-buckets")

			By("Creating the ObjectStorage CR")
			Expect(c.Create(ctx, os)).To(Succeed())

			By("Waiting for Available condition")
			readyOS, err := helpers.WaitForObjectStorageReady(ctx, c, client.ObjectKeyFromObject(os), osReadyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying bucket specs are defined")
			Expect(readyOS.Spec.Buckets).To(HaveLen(3))
			bucketNames := make([]string, len(readyOS.Spec.Buckets))
			for i, b := range readyOS.Spec.Buckets {
				bucketNames[i] = b.Name
			}
			Expect(bucketNames).To(ContainElements("uploads", "backups", "static-assets"))
		})

		AfterAll(func() {
			if os != nil {
				By("Cleaning up ObjectStorage with buckets")
				_ = c.Delete(ctx, os)
				_ = helpers.WaitForObjectStorageDeleted(ctx, c, client.ObjectKeyFromObject(os), osDeleteTimeout)
			}
		})
	})

	Context("ObjectStorage deletion", func() {
		It("should clean up all owned resources on deletion", func() {
			os := fixtures.SimpleObjectStorage("os-deletion-test")

			By("Creating the ObjectStorage CR")
			Expect(c.Create(ctx, os)).To(Succeed())

			By("Waiting for Available condition")
			_, err := helpers.WaitForObjectStorageReady(ctx, c, client.ObjectKeyFromObject(os), osReadyTimeout)
			Expect(err).NotTo(HaveOccurred())

			volumeName := os.VolumeName()
			credentialsSecretName := os.CredentialsSecretName()
			deploymentName := os.DeploymentName()
			serviceName := os.ServiceName()
			ingressName := os.IngressName()

			By("Deleting the ObjectStorage CR")
			Expect(c.Delete(ctx, os)).To(Succeed())

			By("Waiting for ObjectStorage to be deleted")
			Expect(helpers.WaitForObjectStorageDeleted(ctx, c, client.ObjectKeyFromObject(os), osDeleteTimeout)).To(Succeed())

			By("Verifying the underlying Volume is also deleted")
			vol := &storagev1alpha1.Volume{}
			err = c.Get(ctx, client.ObjectKey{
				Name:      volumeName,
				Namespace: os.Namespace,
			}, vol)
			Expect(err).To(HaveOccurred(), "Volume should be deleted")

			By("Verifying the credentials Secret is also deleted")
			secret := &corev1.Secret{}
			err = c.Get(ctx, client.ObjectKey{
				Name:      credentialsSecretName,
				Namespace: os.Namespace,
			}, secret)
			Expect(err).To(HaveOccurred(), "Secret should be deleted")

			By("Verifying the Deployment is also deleted")
			dep := &appsv1.Deployment{}
			err = c.Get(ctx, client.ObjectKey{
				Name:      deploymentName,
				Namespace: os.Namespace,
			}, dep)
			Expect(err).To(HaveOccurred(), "Deployment should be deleted")

			By("Verifying the Service is also deleted")
			svc := &corev1.Service{}
			err = c.Get(ctx, client.ObjectKey{
				Name:      serviceName,
				Namespace: os.Namespace,
			}, svc)
			Expect(err).To(HaveOccurred(), "Service should be deleted")

			By("Verifying the Ingress is also deleted")
			ingress := &networkingv1.Ingress{}
			err = c.Get(ctx, client.ObjectKey{
				Name:      ingressName,
				Namespace: os.Namespace,
			}, ingress)
			Expect(err).To(HaveOccurred(), "Ingress should be deleted")
		})
	})
})
