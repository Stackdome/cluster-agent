package integration

import (
	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

var _ = Describe("PostgresCluster Addon", Ordered, func() {

	Context("Simple PostgresCluster", func() {
		var pg *addonsv1alpha1.PostgresCluster

		It("should create a minimal PostgresCluster and reach Ready", func() {
			pg = fixtures.SimplePostgresCluster("simple-pg")

			By("Creating the PostgresCluster CR")
			Expect(c.Create(ctx, pg)).To(Succeed())

			By("Waiting for ClusterReady condition")
			readyPg, err := helpers.WaitForPostgresClusterReady(ctx, c, client.ObjectKeyFromObject(pg), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyPg.Status.Phase).To(Equal("Cluster in healthy state"))

			By("Verifying status outputs are populated")
			Expect(readyPg.Status.Outputs).NotTo(BeNil())
			Expect(readyPg.Status.Outputs.ClusterName).NotTo(BeEmpty())
			Expect(readyPg.Status.Outputs.WriteService).NotTo(BeEmpty())
			Expect(readyPg.Status.Outputs.ReadService).NotTo(BeEmpty())

			By("Verifying ClusterConfigurationValid condition")
			Expect(helpers.PostgresClusterHasCondition(readyPg, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionTrue)).To(BeTrue())
		})

		It("should verify the underlying CNPG Cluster was created", func() {
			cnpgCluster := &cnpgv1.Cluster{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      pg.CnpgClusterName(),
				Namespace: pg.Namespace,
			}, cnpgCluster)
			Expect(err).NotTo(HaveOccurred())
			Expect(cnpgCluster.Spec.Instances).To(Equal(2))
		})

		AfterAll(func() {
			if pg != nil {
				By("Cleaning up simple PostgresCluster")
				_ = c.Delete(ctx, pg)
				_ = helpers.WaitForPostgresClusterDeleted(ctx, c, client.ObjectKeyFromObject(pg), deleteTimeout)
			}
		})
	})

	Context("PostgresCluster with databases", func() {
		var pg *addonsv1alpha1.PostgresCluster

		It("should create databases and reach DatabasesApplied", func() {
			pg = fixtures.PostgresClusterWithDatabases("pg-with-dbs")

			By("Creating the PostgresCluster CR")
			Expect(c.Create(ctx, pg)).To(Succeed())

			By("Waiting for ClusterReady condition")
			_, err := helpers.WaitForPostgresClusterReady(ctx, c, client.ObjectKeyFromObject(pg), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for DatabasesApplied condition")
			readyPg, err := helpers.WaitForPostgresClusterCondition(ctx, c, client.ObjectKeyFromObject(pg),
				addonsv1alpha1.DatabasesApplied, metav1.ConditionTrue, readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying database info in status outputs")
			Expect(readyPg.Status.Outputs).NotTo(BeNil())
			Expect(readyPg.Status.Outputs.Databases).NotTo(BeEmpty())

			dbNames := make([]string, len(readyPg.Status.Outputs.Databases))
			for i, db := range readyPg.Status.Outputs.Databases {
				dbNames[i] = db.Name
			}
			Expect(dbNames).To(ContainElements("testdb", "analytics", "app"))
		})

		It("should verify CNPG Database CRs were created", func() {
			dbList := &cnpgv1.DatabaseList{}
			err := c.List(ctx, dbList, client.InNamespace(pg.Namespace))
			Expect(err).NotTo(HaveOccurred())

			dbNames := make([]string, len(dbList.Items))
			for i, db := range dbList.Items {
				dbNames[i] = db.Spec.Name
			}
			Expect(dbNames).To(ContainElements("testdb", "analytics"))
		})

		AfterAll(func() {
			if pg != nil {
				By("Cleaning up PostgresCluster with databases")
				_ = c.Delete(ctx, pg)
				_ = helpers.WaitForPostgresClusterDeleted(ctx, c, client.ObjectKeyFromObject(pg), deleteTimeout)
			}
		})
	})

	Context("PostgresCluster with backup", func() {
		var pg *addonsv1alpha1.PostgresCluster

		It("should configure WAL archiving and scheduled backup", func() {
			pg = fixtures.PostgresClusterWithBackup("pg-with-backup", env.ObjectStoreName)

			By("Creating the PostgresCluster CR")
			Expect(c.Create(ctx, pg)).To(Succeed())

			By("Waiting for ClusterReady condition")
			_, err := helpers.WaitForPostgresClusterReady(ctx, c, client.ObjectKeyFromObject(pg), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create a ScheduledBackup CR owned by the PostgresCluster", func() {
			scheduledBackupName := pg.CnpgClusterName() + "-scheduled-backup"
			sb := &cnpgv1.ScheduledBackup{}
			err := c.Get(ctx, client.ObjectKey{
				Name:      scheduledBackupName,
				Namespace: pg.Namespace,
			}, sb)
			Expect(err).NotTo(HaveOccurred())
			Expect(sb.Spec.Schedule).To(Equal("0 0 0 * * 0"))
			Expect(sb.Spec.Cluster.Name).To(Equal(pg.CnpgClusterName()))

			By("Verifying ScheduledBackup is owned by the PostgresCluster")
			Expect(sb.OwnerReferences).NotTo(BeEmpty())
			found := false
			for _, ref := range sb.OwnerReferences {
				if ref.Name == pg.Name && ref.Kind == "PostgresCluster" {
					found = true
					break
				}
			}
			Expect(found).To(BeTrue(), "ScheduledBackup should be owned by PostgresCluster")
		})

		AfterAll(func() {
			if pg != nil {
				By("Cleaning up PostgresCluster with backup")
				_ = c.Delete(ctx, pg)
				_ = helpers.WaitForPostgresClusterDeleted(ctx, c, client.ObjectKeyFromObject(pg), deleteTimeout)
			}
		})
	})

	Context("PostgresCluster deletion", func() {
		It("should clean up all owned resources on deletion", func() {
			pg := fixtures.SimplePostgresCluster("pg-deletion-test")

			By("Creating the PostgresCluster CR")
			Expect(c.Create(ctx, pg)).To(Succeed())

			By("Waiting for ClusterReady condition")
			_, err := helpers.WaitForPostgresClusterReady(ctx, c, client.ObjectKeyFromObject(pg), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			cnpgClusterName := pg.CnpgClusterName()

			By("Deleting the PostgresCluster CR")
			Expect(c.Delete(ctx, pg)).To(Succeed())

			By("Waiting for PostgresCluster to be deleted")
			Expect(helpers.WaitForPostgresClusterDeleted(ctx, c, client.ObjectKeyFromObject(pg), deleteTimeout)).To(Succeed())

			By("Verifying the underlying CNPG Cluster is also deleted")
			cnpgCluster := &cnpgv1.Cluster{}
			err = c.Get(ctx, client.ObjectKey{
				Name:      cnpgClusterName,
				Namespace: pg.Namespace,
			}, cnpgCluster)
			Expect(err).To(HaveOccurred(), "CNPG Cluster should be deleted")
		})
	})
})
