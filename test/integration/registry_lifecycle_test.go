package integration

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	internaltypes "stackdome.io/cluster-agent/internal/types"
	reg "stackdome.io/cluster-agent/pkg/registry"
	"stackdome.io/cluster-agent/test/integration/bootstrap"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

var _ = Describe("Registry Lifecycle", Ordered, func() {

	Context("Registry bootstrap validation", Ordered, func() {
		var bootstrapReg *registryv1alpha1.ClusterRegistry

		It("should have the bootstrap ClusterRegistry in Running phase", func() {
			bootstrapReg = &registryv1alpha1.ClusterRegistry{}
			err := c.Get(ctx, client.ObjectKey{Name: bootstrap.RegistryName}, bootstrapReg)
			Expect(err).NotTo(HaveOccurred())
			Expect(bootstrapReg.Status.Phase).To(Equal(registryv1alpha1.RegistryPhaseRunning))
		})

		It("should have a Ready condition set to True", func() {
			Expect(helpers.ClusterRegistryHasCondition(bootstrapReg, string(registryv1alpha1.RegistryReady), metav1.ConditionTrue)).To(BeTrue())
		})

		It("should have the registry StatefulSet available", func() {
			sts := &appsv1.StatefulSet{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      bootstrap.RegistryName,
				Namespace: bootstrap.RegistryNamespace,
			}, sts)
			Expect(err).NotTo(HaveOccurred())
			Expect(sts.Status.ReadyReplicas).To(BeNumerically(">=", 1))
		})

		It("should have the registry Service with a ClusterIP", func() {
			svc := &corev1.Service{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      bootstrap.RegistryName,
				Namespace: bootstrap.RegistryNamespace,
			}, svc)
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Spec.ClusterIP).NotTo(BeEmpty())
		})

		It("should have the registry PVC created by the StatefulSet", func() {
			pvc := &corev1.PersistentVolumeClaim{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      "storage-" + bootstrap.RegistryName + "-0",
				Namespace: bootstrap.RegistryNamespace,
			}, pvc)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have the registry ConfigMap with valid Zot config", func() {
			cm := &corev1.ConfigMap{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      bootstrapReg.RegistryConfigMapName(),
				Namespace: bootstrap.RegistryNamespace,
			}, cm)
			Expect(err).NotTo(HaveOccurred())
			Expect(cm.Data["config.json"]).NotTo(BeEmpty())

			var config map[string]interface{}
			Expect(json.Unmarshal([]byte(cm.Data["config.json"]), &config)).To(Succeed())
			Expect(config).To(HaveKey("http"))
			Expect(config).To(HaveKey("storage"))
		})

		It("should have the shared insecure-registries ConfigMap with the registry endpoint", func() {
			cm := &corev1.ConfigMap{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      "stackdome-insecure-registries",
				Namespace: bootstrap.RegistryNamespace,
			}, cm)
			Expect(err).NotTo(HaveOccurred())

			var registryConfig internaltypes.RegistryConfig
			Expect(json.Unmarshal([]byte(cm.Data["registries.json"]), &registryConfig)).To(Succeed())
			Expect(registryConfig.HasEndpoint(bootstrapReg.Status.InternalURL)).To(BeTrue())
		})

		It("should have the registry-config-reconciler DaemonSet with security context", func() {
			ds := &appsv1.DaemonSet{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      reg.RegistryConfigReconcilerDaemonSetName,
				Namespace: bootstrap.RegistryNamespace,
			}, ds)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying HostPID is set (required to SIGHUP containerd)")
			Expect(ds.Spec.Template.Spec.HostPID).To(BeTrue())

			By("Verifying pod security context")
			podSC := ds.Spec.Template.Spec.SecurityContext
			Expect(podSC).NotTo(BeNil())
			Expect(podSC.RunAsUser).NotTo(BeNil())
			Expect(*podSC.RunAsUser).To(Equal(int64(0)))

			By("Verifying container security context")
			containerSC := ds.Spec.Template.Spec.Containers[0].SecurityContext
			Expect(containerSC).NotTo(BeNil())
			Expect(containerSC.AllowPrivilegeEscalation).NotTo(BeNil())
			Expect(*containerSC.AllowPrivilegeEscalation).To(BeFalse())
			Expect(containerSC.Capabilities).NotTo(BeNil())
			Expect(containerSC.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
		})
	})

	Context("Registry credential stability", Ordered, func() {
		It("should not trigger spurious StatefulSet rollouts on reconcile", func() {
			sts := &appsv1.StatefulSet{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      bootstrap.RegistryName,
				Namespace: bootstrap.RegistryNamespace,
			}, sts)
			Expect(err).NotTo(HaveOccurred())
			initialVersion := sts.ResourceVersion

			By("Waiting 30 seconds for multiple reconcile loops")
			time.Sleep(30 * time.Second)

			err = c.Get(ctx, client.ObjectKey{
				Name:      bootstrap.RegistryName,
				Namespace: bootstrap.RegistryNamespace,
			}, sts)
			Expect(err).NotTo(HaveOccurred())
			Expect(sts.ResourceVersion).To(Equal(initialVersion),
				"StatefulSet ResourceVersion should not change across reconcile loops (bcrypt stability fix)")
		})
	})

	Context("Registry credential update", Ordered, func() {
		var initialAuthSecretVersion string

		It("should record the current auth secret version", func() {
			authSecret := &corev1.Secret{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      bootstrap.RegistryName + "-auth",
				Namespace: bootstrap.RegistryNamespace,
			}, authSecret)
			Expect(err).NotTo(HaveOccurred())
			initialAuthSecretVersion = authSecret.ResourceVersion
		})

		It("should update the auth secret when credentials change", func() {
			By("Updating the credentials secret with a new password")
			credSecret := &corev1.Secret{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      "registry-creds",
				Namespace: bootstrap.RegistryNamespace,
			}, credSecret)
			Expect(err).NotTo(HaveOccurred())

			credSecret.Data["password"] = []byte("new-password-123")
			Expect(c.Update(ctx, credSecret)).To(Succeed())

			DeferCleanup(func() {
				By("Restoring original registry credentials")
				restored := &corev1.Secret{}
				if err := c.Get(ctx, client.ObjectKey{Name: "registry-creds", Namespace: bootstrap.RegistryNamespace}, restored); err != nil {
					return
				}
				restored.Data["password"] = []byte("admin")
				_ = c.Update(ctx, restored)

				r := &registryv1alpha1.ClusterRegistry{}
				if err := c.Get(ctx, client.ObjectKey{Name: bootstrap.RegistryName}, r); err != nil {
					return
				}
				if r.Annotations == nil {
					r.Annotations = map[string]string{}
				}
				r.Annotations["test.stackdome.io/credential-update"] = time.Now().Format(time.RFC3339)
				_ = c.Update(ctx, r)
			})

			By("Forcing a reconcile by annotating the ClusterRegistry CR")
			reg := &registryv1alpha1.ClusterRegistry{}
			Expect(c.Get(ctx, client.ObjectKey{Name: bootstrap.RegistryName}, reg)).To(Succeed())
			if reg.Annotations == nil {
				reg.Annotations = map[string]string{}
			}
			reg.Annotations["test.stackdome.io/credential-update"] = time.Now().Format(time.RFC3339)
			Expect(c.Update(ctx, reg)).To(Succeed())

			By("Waiting for the auth secret to be updated")
			Eventually(func() bool {
				authSecret := &corev1.Secret{}
				if err := c.Get(ctx, client.ObjectKey{
					Name:      bootstrap.RegistryName + "-auth",
					Namespace: bootstrap.RegistryNamespace,
				}, authSecret); err != nil {
					return false
				}
				return authSecret.ResourceVersion != initialAuthSecretVersion
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(),
				"auth secret should be updated after credential change")
		})
	})

	Context("Second registry lifecycle and deletion cleanup", Ordered, func() {
		const (
			secondRegistryName = "e2e-second-registry"
			secondCredSecret   = "e2e-second-registry-creds"
			secondPort         = int32(5001)
		)

		var secondReg *registryv1alpha1.ClusterRegistry

		It("should create a second registry and reach Running", func() {
			By("Creating credentials secret for second registry")
			credSecret := fixtures.ClusterRegistryCredentialsSecret(
				secondCredSecret, bootstrap.RegistryNamespace, "testuser", "testpass")
			Expect(c.Create(ctx, credSecret)).To(Succeed())

			By("Creating second ClusterRegistry CR")
			secondReg = fixtures.SimpleClusterRegistry(secondRegistryName, secondCredSecret, secondPort)
			Expect(c.Create(ctx, secondReg)).To(Succeed())

			By("Waiting for second registry to reach Running")
			readyReg, err := helpers.WaitForClusterRegistryReady(
				ctx, c, client.ObjectKey{Name: secondRegistryName}, readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyReg.Status.InternalURL).NotTo(BeEmpty())
			secondReg = readyReg
		})

		It("should have both registries in the shared ConfigMap", func() {
			cm := &corev1.ConfigMap{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      "stackdome-insecure-registries",
				Namespace: bootstrap.RegistryNamespace,
			}, cm)
			Expect(err).NotTo(HaveOccurred())

			var registryConfig internaltypes.RegistryConfig
			Expect(json.Unmarshal([]byte(cm.Data["registries.json"]), &registryConfig)).To(Succeed())

			bootstrapReg := &registryv1alpha1.ClusterRegistry{}
			Expect(c.Get(ctx, client.ObjectKey{Name: bootstrap.RegistryName}, bootstrapReg)).To(Succeed())
			Expect(registryConfig.HasEndpoint(bootstrapReg.Status.InternalURL)).To(BeTrue(),
				"shared ConfigMap should contain bootstrap registry endpoint")
			Expect(registryConfig.HasEndpoint(secondReg.Status.InternalURL)).To(BeTrue(),
				"shared ConfigMap should contain second registry endpoint")
		})

		It("should clean up shared ConfigMap entry on second registry deletion", func() {
			secondRegURL := secondReg.Status.InternalURL

			By("Deleting the second ClusterRegistry")
			Expect(c.Delete(ctx, secondReg)).To(Succeed())

			By("Waiting for second registry to be deleted")
			Expect(helpers.WaitForClusterRegistryDeleted(
				ctx, c, client.ObjectKey{Name: secondRegistryName}, deleteTimeout)).To(Succeed())

			By("Verifying shared ConfigMap only has the bootstrap registry")
			Eventually(func() bool {
				cm := &corev1.ConfigMap{}
				if err := c.Get(ctx, client.ObjectKey{
					Name:      "stackdome-insecure-registries",
					Namespace: bootstrap.RegistryNamespace,
				}, cm); err != nil {
					return false
				}
				var registryConfig internaltypes.RegistryConfig
				if err := json.Unmarshal([]byte(cm.Data["registries.json"]), &registryConfig); err != nil {
					return false
				}
				return !registryConfig.HasEndpoint(secondRegURL)
			}, 1*time.Minute, 5*time.Second).Should(BeTrue(),
				"deleted registry endpoint should be removed from shared ConfigMap")
		})

		It("should keep the DaemonSet running since the bootstrap registry still exists", func() {
			ds := &appsv1.DaemonSet{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      reg.RegistryConfigReconcilerDaemonSetName,
				Namespace: bootstrap.RegistryNamespace,
			}, ds)
			Expect(err).NotTo(HaveOccurred(), "DaemonSet should still exist while bootstrap registry is running")
		})

		AfterAll(func() {
			credSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secondCredSecret,
					Namespace: bootstrap.RegistryNamespace,
				},
			}
			_ = c.Delete(ctx, credSecret)
		})
	})
})
