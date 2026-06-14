package integration

import (
	"context"
	"fmt"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/bootstrap"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

const (
	migStackReadyTimeout   = 5 * time.Minute
	migSRAvailTimeout      = 5 * time.Minute
	migFieldRemovalTimeout = 2 * time.Minute
	migNoopStabilityWindow = 30 * time.Second
	migPGReadyTimeout      = 5 * time.Minute
	migCNPGChangeTimeout   = 2 * time.Minute
	migStackDeleteTimeout  = 2 * time.Minute
	migPGDeleteTimeout     = 2 * time.Minute
	migRegistryTimeout     = 3 * time.Minute
)

var _ = Describe("SSA to Update Migration", Ordered, func() {
	var (
		testEnv *bootstrap.Environment
		ctx     context.Context
		c       client.Client
	)

	BeforeAll(func() {
		testEnv = GetEnvironment()
		ctx = context.Background()
		c = testEnv.Client
	})

	Context("StackResource field removal (workload_reconciler)", Ordered, func() {
		var (
			stack        *corev1alpha1.Stack
			resourceName string
			depKey       client.ObjectKey
		)

		BeforeAll(func() {
			stack = fixtures.StackWithAllOptionalFields("mig-stack")
			resourceName = stack.Spec.StackResources[0].Name

			By("Creating the Stack with all optional fields populated")
			Expect(c.Create(ctx, stack)).To(Succeed())

			By("Waiting for Stack to become Ready")
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), migStackReadyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for StackResource to become Available")
			_, err = helpers.WaitForStackResourceAvailable(ctx, c, client.ObjectKey{
				Name:      resourceName,
				Namespace: stack.Namespace,
			}, migSRAvailTimeout)
			Expect(err).NotTo(HaveOccurred())

			depKey = client.ObjectKey{Name: resourceName, Namespace: stack.Namespace}
		})

		It("should have all optional fields populated on the Deployment (pre-condition)", func() {
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())

			container := dep.Spec.Template.Spec.Containers[0]
			Expect(container.Env).NotTo(BeEmpty(), "env vars should be populated")
			Expect(container.Ports).NotTo(BeEmpty(), "ports should be populated")
			Expect(container.Command).NotTo(BeEmpty(), "command should be populated")
			Expect(container.Args).NotTo(BeEmpty(), "args should be populated")
			Expect(dep.Spec.Template.Spec.InitContainers).NotTo(BeEmpty(), "init containers should be populated")
		})

		It("should remove env vars from Deployment when EnvironmentVariables are cleared", func() {
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())
			previousRV := dep.ResourceVersion

			By("Clearing EnvironmentVariables on the Stack")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())
			stack.Spec.StackResources[0].Spec.EnvironmentVariables = nil
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for Deployment to be updated")
			updatedDep, err := helpers.WaitForDeploymentFieldChange(ctx, c, depKey, previousRV, migFieldRemovalTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying env vars are removed")
			Expect(updatedDep.Spec.Template.Spec.Containers[0].Env).To(BeEmpty(),
				"env vars should be removed from Deployment after clearing EnvironmentVariables")
		})

		It("should remove container ports from Deployment when Ports are cleared", func() {
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())
			previousRV := dep.ResourceVersion

			By("Clearing Ports on the Stack")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())
			stack.Spec.StackResources[0].Spec.Ports = nil
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for Deployment to be updated")
			updatedDep, err := helpers.WaitForDeploymentFieldChange(ctx, c, depKey, previousRV, migFieldRemovalTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying container ports are removed")
			Expect(updatedDep.Spec.Template.Spec.Containers[0].Ports).To(BeEmpty(),
				"container ports should be removed from Deployment after clearing Ports")
		})

		It("should remove command from Deployment when Command is cleared", func() {
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())
			previousRV := dep.ResourceVersion

			By("Clearing Command on the Stack")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())
			stack.Spec.StackResources[0].Spec.Command = nil
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for Deployment to be updated")
			updatedDep, err := helpers.WaitForDeploymentFieldChange(ctx, c, depKey, previousRV, migFieldRemovalTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying command is removed")
			Expect(updatedDep.Spec.Template.Spec.Containers[0].Command).To(BeEmpty(),
				"command should be removed from Deployment after clearing Command")
		})

		It("should remove args from Deployment when Args are cleared", func() {
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())
			previousRV := dep.ResourceVersion

			By("Clearing Args on the Stack")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())
			stack.Spec.StackResources[0].Spec.Args = nil
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for Deployment to be updated")
			updatedDep, err := helpers.WaitForDeploymentFieldChange(ctx, c, depKey, previousRV, migFieldRemovalTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying args are removed")
			Expect(updatedDep.Spec.Template.Spec.Containers[0].Args).To(BeEmpty(),
				"args should be removed from Deployment after clearing Args")
		})

		It("should remove init containers from Deployment when Init is set to nil", func() {
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())
			previousRV := dep.ResourceVersion

			By("Setting Init to nil on the Stack")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), stack)).To(Succeed())
			stack.Spec.StackResources[0].Spec.Init = nil
			Expect(c.Update(ctx, stack)).To(Succeed())

			By("Waiting for Deployment to be updated")
			updatedDep, err := helpers.WaitForDeploymentFieldChange(ctx, c, depKey, previousRV, migFieldRemovalTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying init containers are removed")
			Expect(updatedDep.Spec.Template.Spec.InitContainers).To(BeEmpty(),
				"init containers should be removed from Deployment after clearing Init")
		})

		It("should not update Deployment when StackResource spec is unchanged (no spurious updates)", func() {
			By("Waiting for Deployment to stabilize")
			time.Sleep(10 * time.Second)

			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())
			stableRV := dep.ResourceVersion

			By("Triggering a no-op reconcile by annotating the StackResource")
			sr := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: resourceName, Namespace: stack.Namespace}, sr)).To(Succeed())
			if sr.Annotations == nil {
				sr.Annotations = make(map[string]string)
			}
			sr.Annotations["ssa-test/noop"] = fmt.Sprintf("%d", time.Now().UnixNano())
			Expect(c.Update(ctx, sr)).To(Succeed())

			By("Verifying Deployment ResourceVersion remains stable for 30 seconds")
			err = helpers.VerifyResourceVersionStable(ctx, c, depKey, stableRV, migNoopStabilityWindow)
			Expect(err).NotTo(HaveOccurred(), "Deployment should not be updated when spec is unchanged")
		})

		AfterAll(func() {
			// Skipping cleanup for debugging — leave Stack in cluster
		})
	})

	Context("PostgresCluster field removal (pg_cluster_reconciler)", Ordered, func() {
		var (
			pg      *addonsv1alpha1.PostgresCluster
			cnpgKey client.ObjectKey
		)

		BeforeAll(func() {
			pg = fixtures.PostgresClusterWithCustomConfig("mig-pg")

			By("Creating PostgresCluster with sync replicas and custom config")
			Expect(c.Create(ctx, pg)).To(Succeed())

			By("Waiting for PostgresCluster to become Ready")
			_, err := helpers.WaitForPostgresClusterReady(ctx, c, client.ObjectKeyFromObject(pg), migPGReadyTimeout)
			Expect(err).NotTo(HaveOccurred())

			cnpgKey = client.ObjectKey{
				Name:      pg.CnpgClusterName(),
				Namespace: pg.Namespace,
			}
		})

		It("should have synchronous replication and postgres params configured (pre-condition)", func() {
			cnpgCluster := &cnpgv1.Cluster{}
			Expect(c.Get(ctx, cnpgKey, cnpgCluster)).To(Succeed())

			Expect(cnpgCluster.Spec.PostgresConfiguration.Synchronous).NotTo(BeNil(),
				"synchronous replication should be configured")
			Expect(cnpgCluster.Spec.PostgresConfiguration.Parameters).NotTo(BeEmpty(),
				"postgres parameters should be configured")
			Expect(cnpgCluster.Spec.PostgresConfiguration.Parameters["max_connections"]).To(Equal("200"))
			Expect(cnpgCluster.Spec.PostgresConfiguration.Parameters["shared_buffers"]).To(Equal("256MB"))
		})

		It("should remove synchronous replication config when NumSynchronousReplicas set to 0", func() {
			cnpgCluster := &cnpgv1.Cluster{}
			Expect(c.Get(ctx, cnpgKey, cnpgCluster)).To(Succeed())
			previousRV := cnpgCluster.ResourceVersion

			By("Setting NumSynchronousReplicas to 0")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(pg), pg)).To(Succeed())
			pg.Spec.ReplicasSpec.NumSynchronousReplicas = 0
			Expect(c.Update(ctx, pg)).To(Succeed())

			By("Waiting for CNPG Cluster to be updated")
			updatedCluster, err := helpers.WaitForCNPGClusterSpecChange(ctx, c, cnpgKey, previousRV, migCNPGChangeTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying synchronous replication config is removed")
			Expect(updatedCluster.Spec.PostgresConfiguration.Synchronous).To(BeNil(),
				"synchronous replication should be removed when NumSynchronousReplicas is 0")

			By("Waiting for PostgresCluster to become Ready again")
			_, err = helpers.WaitForPostgresClusterReady(ctx, c, client.ObjectKeyFromObject(pg), migPGReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should remove custom postgres params when PostgresConf is emptied", func() {
			cnpgCluster := &cnpgv1.Cluster{}
			Expect(c.Get(ctx, cnpgKey, cnpgCluster)).To(Succeed())
			previousRV := cnpgCluster.ResourceVersion

			By("Clearing PostgresConf")
			Expect(c.Get(ctx, client.ObjectKeyFromObject(pg), pg)).To(Succeed())
			pg.Spec.PostgreSQLSpec.PostgresConf = nil
			Expect(c.Update(ctx, pg)).To(Succeed())

			By("Waiting for CNPG Cluster to be updated")
			updatedCluster, err := helpers.WaitForCNPGClusterSpecChange(ctx, c, cnpgKey, previousRV, migCNPGChangeTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying custom postgres parameters are removed (CNPG defaults may remain)")
			Expect(updatedCluster.Spec.PostgresConfiguration.Parameters).NotTo(HaveKey("max_connections"),
				"max_connections should be removed when PostgresConf is emptied")
			Expect(updatedCluster.Spec.PostgresConfiguration.Parameters).NotTo(HaveKey("shared_buffers"),
				"shared_buffers should be removed when PostgresConf is emptied")

			By("Waiting for PostgresCluster to become Ready again")
			_, err = helpers.WaitForPostgresClusterReady(ctx, c, client.ObjectKeyFromObject(pg), migPGReadyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should not update CNPG Cluster spec when PostgresCluster spec is unchanged (no spurious updates)", func() {
			By("Waiting for CNPG Cluster to stabilize")
			time.Sleep(10 * time.Second)

			cnpgCluster := &cnpgv1.Cluster{}
			Expect(c.Get(ctx, cnpgKey, cnpgCluster)).To(Succeed())
			specBefore := cnpgCluster.Spec.DeepCopy()

			By("Triggering a no-op reconcile by patching an annotation on the PostgresCluster")
			basePG := pg.DeepCopy()
			if pg.Annotations == nil {
				pg.Annotations = make(map[string]string)
			}
			pg.Annotations["ssa-test/noop"] = fmt.Sprintf("%d", time.Now().UnixNano())
			Expect(c.Patch(ctx, pg, client.MergeFrom(basePG))).To(Succeed())

			By("Waiting for reconciliation to run")
			time.Sleep(15 * time.Second)

			By("Verifying CNPG Cluster spec is unchanged")
			updatedCluster := &cnpgv1.Cluster{}
			Expect(c.Get(ctx, cnpgKey, updatedCluster)).To(Succeed())
			Expect(updatedCluster.Spec).To(Equal(*specBefore),
				"CNPG Cluster spec should not change when PostgresCluster spec is unchanged")
		})

		AfterAll(func() {
			if pg != nil {
				By("Cleaning up migration test PostgresCluster")
				_ = c.Delete(ctx, pg)
				_ = helpers.WaitForPostgresClusterDeleted(ctx, c, client.ObjectKeyFromObject(pg), migPGDeleteTimeout)
			}
		})
	})

	Context("Registry controller (no spurious updates)", Ordered, func() {
		const registryName = "int-test-registry"

		It("should not update registry Deployment when nothing changed", func() {
			By("Verifying registry is running (set up by bootstrap)")
			reg, err := helpers.WaitForRegistryReady(ctx, c, registryName, migRegistryTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(reg.Status.Phase).To(Equal(registryv1alpha1.RegistryPhaseRunning))

			By("Recording registry Deployment ResourceVersion")
			dep := &appsv1.Deployment{}
			Expect(c.Get(ctx, client.ObjectKey{
				Name:      registryName,
				Namespace: "stackdome-registry",
			}, dep)).To(Succeed())
			stableRV := dep.ResourceVersion

			By("Triggering a no-op reconcile by annotating the ClusterRegistry")
			Expect(c.Get(ctx, client.ObjectKey{Name: registryName}, reg)).To(Succeed())
			if reg.Annotations == nil {
				reg.Annotations = make(map[string]string)
			}
			reg.Annotations["ssa-test/noop"] = fmt.Sprintf("%d", time.Now().UnixNano())
			Expect(c.Update(ctx, reg)).To(Succeed())

			By("Verifying registry Deployment ResourceVersion remains stable")
			depKey := client.ObjectKey{Name: registryName, Namespace: "stackdome-registry"}
			err = helpers.VerifyResourceVersionStable(ctx, c, depKey, stableRV, migNoopStabilityWindow)
			Expect(err).NotTo(HaveOccurred(), "Registry Deployment should not be updated on no-op reconcile")
		})

		It("should not update registry ConfigMap when nothing changed", func() {
			By("Recording registry ConfigMap ResourceVersion")
			cm := &corev1.ConfigMap{}
			cmKey := client.ObjectKey{
				Name:      registryName + "-config",
				Namespace: "stackdome-registry",
			}
			Expect(c.Get(ctx, cmKey, cm)).To(Succeed())
			stableRV := cm.ResourceVersion

			By("Triggering a no-op reconcile by annotating the ClusterRegistry")
			reg := &registryv1alpha1.ClusterRegistry{}
			Expect(c.Get(ctx, client.ObjectKey{Name: registryName}, reg)).To(Succeed())
			if reg.Annotations == nil {
				reg.Annotations = make(map[string]string)
			}
			reg.Annotations["ssa-test/noop-cm"] = fmt.Sprintf("%d", time.Now().UnixNano())
			Expect(c.Update(ctx, reg)).To(Succeed())

			By("Waiting for reconciliation to happen")
			time.Sleep(15 * time.Second)

			By("Verifying ConfigMap ResourceVersion is unchanged")
			updatedCM := &corev1.ConfigMap{}
			Expect(c.Get(ctx, cmKey, updatedCM)).To(Succeed())
			Expect(updatedCM.ResourceVersion).To(Equal(stableRV),
				"Registry ConfigMap should not be updated on no-op reconcile")
		})
	})
})
