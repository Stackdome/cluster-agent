package fixtures

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
)

const (
	defaultImageCatalog = "postgres-catalog"
	defaultNamespace    = "pg-integration-test"
	defaultStorageClass = "standard"
	defaultStorageSize  = "1Gi"
	defaultPGMajor      = 16
	defaultPGMinor      = 3
)

// SimplePostgresCluster creates a minimal 1-instance PostgresCluster.
func SimplePostgresCluster(name string) *addonsv1alpha1.PostgresCluster {
	return &addonsv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
		},
		Spec: addonsv1alpha1.PostgresClusterSpec{
			Instances: 2,
			ReplicasSpec: addonsv1alpha1.ReplicasSpec{
				NumSynchronousReplicas:           1,
				SynchronousReplicaDataDurability: addonsv1alpha1.PreferredDataDurability,
			},
			PostgreSQLSpec: &addonsv1alpha1.PostgreSQLSpec{
				ImageCatalogRef: &addonsv1alpha1.ImageCatalogRef{
					Name: defaultImageCatalog,
				},
				PostgreSQLMajorVersion: defaultPGMajor,
				PostgreSQLMinorVersion: defaultPGMinor,
			},
			StorageSpec: &addonsv1alpha1.StorageSpec{
				StorageClassName: defaultStorageClass,
				Size:             defaultStorageSize,
			},
		},
	}
}

// PostgresClusterWithDatabases creates a PostgresCluster with named databases.
func PostgresClusterWithDatabases(name string) *addonsv1alpha1.PostgresCluster {
	pg := SimplePostgresCluster(name)
	pg.Spec.Databases = []addonsv1alpha1.DatabaseSpec{
		{
			Name: "testdb",
			Extensions: []addonsv1alpha1.ExtensionSpec{
				{Name: addonsv1alpha1.Pgvector},
			},
		},
		{
			Name: "analytics",
		},
	}
	return pg
}

// PostgresClusterWithBackup creates a PostgresCluster with backup configured
// pointing to the S3Mock-backed ObjectStore.
func PostgresClusterWithBackup(name, objectStoreName string) *addonsv1alpha1.PostgresCluster {
	pg := SimplePostgresCluster(name)
	pg.Spec.ClusterBackupSpec = &addonsv1alpha1.ClusterBackupSpec{
		WalArchivingEnabled: true,
		ObjectStoreName:     objectStoreName,
		ScheduledBaseBackupSpec: &addonsv1alpha1.ScheduledBaseBackupSpec{
			Schedule: "0 0 0 * * 0",
			Enabled:  true,
		},
	}
	return pg
}
