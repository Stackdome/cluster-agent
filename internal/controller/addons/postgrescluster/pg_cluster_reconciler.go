package postgrescluster

import (
	"context"
	"fmt"
	"strings"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	k8sapierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

const (
	// ReconcilerName is the name of the PostgresCluster reconciler.
	pgClusterReconcilerName = "cnpg-cluster-reconciler"
)

type pgClusterReconciler struct {
	client client.Client
	Scheme *runtime.Scheme
}

func newPgClusterReconciler(client client.Client, scheme *runtime.Scheme) *pgClusterReconciler {
	return &pgClusterReconciler{
		client: client,
		Scheme: scheme,
	}
}

func (r *pgClusterReconciler) name() string {
	return pgClusterReconcilerName
}

func (r *pgClusterReconciler) reconcile(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger = logger.WithName(pgClusterReconcilerName)
	ctx = controller.ContextWithLogger(ctx, logger)

	imageCatalog := &cnpgv1.ImageCatalog{}
	err := r.client.Get(ctx, client.ObjectKey{
		Name:      resource.Spec.PostgreSQLSpec.ImageCatalogRef.Name,
		Namespace: resource.Namespace,
	}, imageCatalog)
	if err != nil {
		return resultNil, err
	}

	catalogImage, exists := lo.Find(imageCatalog.Spec.Images, func(imagectlg cnpgv1.CatalogImage) bool {
		return imagectlg.Major == resource.Spec.PostgreSQLSpec.PostgreSQLMajorVersion
	})
	if !exists {
		logger.Error(fmt.Errorf("image not found in catalog"), "Image not found in catalog",
			"catalog", resource.Spec.PostgreSQLSpec.ImageCatalogRef.Name,
			"version", resource.Spec.PostgreSQLSpec.PostgreSQLMajorVersion,
		)
		return resultNil, fmt.Errorf("image not found in catalog %s for version %d", resource.Spec.PostgreSQLSpec.ImageCatalogRef.Name, resource.Spec.PostgreSQLSpec.PostgreSQLMajorVersion)
	}

	desiredcnpgCluster := buildDesiredCluster(resource, catalogImage.Image)
	desiredcnpgCluster.SetGroupVersionKind(cnpgv1.SchemeGroupVersion.WithKind("Cluster"))
	if err := controllerutil.SetControllerReference(resource, desiredcnpgCluster, r.Scheme); err != nil {
		return resultNil, err
	}

	existingPgCluster := &cnpgv1.Cluster{}
	err = r.client.Get(ctx, client.ObjectKey{
		Name:      resource.CnpgClusterName(),
		Namespace: resource.Namespace,
	}, existingPgCluster)
	if err != nil {
		if k8sapierrors.IsNotFound(err) {
			return resultRequeue, r.client.Create(ctx, desiredcnpgCluster)
		}
		return resultNil, fmt.Errorf("failed to get existing PostgresCluster: %w", err)
	}

	if existingPgCluster.Spec.ImageName != desiredcnpgCluster.Spec.ImageName {
		// We dont update the postgres image in this reconciler, we have a separate reconciler for updates.
		desiredcnpgCluster.Spec.ImageName = existingPgCluster.Spec.ImageName
	}

	// Check if we are downsizing the storage size.
	// If so, we do not allow it.
	// TODO: Move this to a validation webhook.
	desiredStorageQuantity, err := k8sresource.ParseQuantity(desiredcnpgCluster.Spec.StorageConfiguration.Size)
	if err != nil {
		return resultNil, fmt.Errorf("failed to parse storage size %s: %w", desiredcnpgCluster.Spec.StorageConfiguration.Size, err)
	}
	existingStorageQuantity, err := k8sresource.ParseQuantity(existingPgCluster.Spec.StorageConfiguration.Size)
	if err != nil {
		return resultNil, fmt.Errorf("failed to parse existing storage size %s: %w", existingPgCluster.Spec.StorageConfiguration.Size, err)
	}

	if desiredStorageQuantity.Cmp(existingStorageQuantity) == -1 {
		// We do not allow shrinking the storage size.
		setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "InvalidStorageSize", "Desired storage size is less than existing")
		setPhase(resource, addonsv1alpha1.ErrorPhase)
		return resultStop, nil
	}

	if err := r.client.Patch(ctx, desiredcnpgCluster, client.Apply, &client.PatchOptions{
		Force:        ptr.To(true),
		FieldManager: postgresClusterController,
	}); err != nil {
		return resultNil, err
	}

	// refetch the existing cluster to get the latest status.
	existingPgCluster = &cnpgv1.Cluster{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Name:      resource.CnpgClusterName(),
		Namespace: resource.Namespace,
	}, existingPgCluster); err != nil {
		return resultNil, err
	}

	readyCond := meta.FindStatusCondition(existingPgCluster.Status.Conditions, string(cnpgv1.ConditionClusterReady))
	switch {
	case readyCond == nil:
		logger.Info("PostgresCluster is not ready, cnpg ready condition missing", "name", resource.Name)
		setPhase(resource, existingPgCluster.Status.Phase)
		setStatusCondition(resource, addonsv1alpha1.ClusterReady, metav1.ConditionFalse, "NotReady", "PostgresCluster is not ready")
		return resultStop, nil
	case readyCond.Status != metav1.ConditionTrue:
		logger.Info("PostgresCluster is not ready, cnpg ready condition not true or not uptodate", "name", resource.Name)
		setStatusCondition(resource, addonsv1alpha1.ClusterReady, metav1.ConditionFalse, "NotReady", "PostgresCluster is not ready")
		setPhase(resource, existingPgCluster.Status.Phase)
		return resultStop, nil
	case readyCond.Status == metav1.ConditionTrue:
		logger.Info("PostgresCluster is ready", "name", resource.Name)
		setStatusCondition(resource, addonsv1alpha1.ClusterReady, metav1.ConditionTrue, "Ready", "PostgresCluster is ready")
		setPhase(resource, existingPgCluster.Status.Phase)
		if err := r.populateStatus(ctx, resource, existingPgCluster); err != nil {
			return resultNil, err
		}
	}
	return resultNil, nil
}

func (r *pgClusterReconciler) populateStatus(ctx context.Context, resource *addonsv1alpha1.PostgresCluster, cnpgCluster *cnpgv1.Cluster) error {
	resource.Status.CurrentImage = cnpgCluster.Spec.ImageName
	resource.Status.CurrentPostgreSQLMajorVersion = fmt.Sprintf("%d", resource.Spec.PostgreSQLSpec.PostgreSQLMajorVersion)
	resource.Status.CurrentPostgreSQLMinorVersion = fmt.Sprintf("%d", resource.Spec.PostgreSQLSpec.PostgreSQLMinorVersion)
	writeServiceHost := fmt.Sprintf("%s.%s.svc.cluster.local", cnpgCluster.Status.WriteService, cnpgCluster.Namespace)
	resource.Status.Outputs = &addonsv1alpha1.PostgresClusterOutputs{
		ClusterName:           cnpgCluster.Name,
		ReadService:           fmt.Sprintf("%s.%s.svc.cluster.local", cnpgCluster.Status.ReadService, cnpgCluster.Namespace),
		WriteService:          writeServiceHost,
		UserCredentialSecrets: make(map[string]string, 0),
		ClusterConnection: &addonsv1alpha1.ClusterConnectionInfo{
			Host:    writeServiceHost,
			Port:    5432,
			SSLMode: "require",
		},
	}
	secretList := &corev1.SecretList{}
	if err := r.client.List(ctx, secretList, client.InNamespace(resource.Namespace)); err != nil {
		return fmt.Errorf("failed to list secrets in namespace %s: %w", resource.Namespace, err)
	}

	for _, secret := range secretList.Items {
		switch {
		case secret.Type == corev1.SecretTypeBasicAuth && secret.Name == fmt.Sprintf("%s-superuser", cnpgCluster.Name):
			resource.Status.Outputs.SuperUserCredentialSecret = &secret.Name
		case secret.Name == fmt.Sprintf("%s-ca", cnpgCluster.Name):
			resource.Status.Outputs.ClientCASecret = secret.Name
		case secret.Type == corev1.SecretTypeBasicAuth && strings.HasPrefix(secret.Name, cnpgCluster.Name):
			user := string(secret.Data["username"])
			resource.Status.Outputs.UserCredentialSecrets[user] = secret.Name
		}
	}
	return nil
}

func buildDesiredCluster(resource *addonsv1alpha1.PostgresCluster, pgImage string) *cnpgv1.Cluster {
	// Create a new cnpgv1.Cluster object based on the resource.
	desiredCluster := &cnpgv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resource.CnpgClusterName(),
			Namespace: resource.Namespace,
		},
		Spec: cnpgv1.ClusterSpec{
			Instances: resource.Spec.Instances,
			ImageName: pgImage,
			StorageConfiguration: cnpgv1.StorageConfiguration{
				StorageClass: &resource.Spec.StorageSpec.StorageClassName,
				Size:         resource.Spec.StorageSpec.Size,
			},
			Bootstrap:             buildBootstrapSpec(resource),
			ExternalClusters:      buildExternalClusters(resource),
			Plugins:               buildPluginsSpec(resource),
			PostgresConfiguration: buildPostgresConfiguration(resource),
			Affinity:              buildAffinityConfiguration(resource),
			EnableSuperuserAccess: ptr.To(resource.Spec.EnableSuperuserAccess),
			Resources:             resource.Spec.ResourceSpec,
			EnablePDB: func() *bool {
				// If the resource has only one instance, we disable PDB.
				var enablePDB bool
				if resource.Spec.Instances == 1 {
					enablePDB = false
					return &enablePDB
				}
				enablePDB = true
				return &enablePDB
			}(),
		},
	}
	return desiredCluster
}

func buildAffinityConfiguration(resource *addonsv1alpha1.PostgresCluster) cnpgv1.AffinityConfiguration {
	instancePlacementSpec := resource.Spec.InstancePlacementSpec
	if instancePlacementSpec == nil {
		return cnpgv1.AffinityConfiguration{}
	}
	return cnpgv1.AffinityConfiguration{
		TopologyKey:         instancePlacementSpec.TopologyKey,
		NodeSelector:        instancePlacementSpec.NodeSelector,
		Tolerations:         instancePlacementSpec.Tolerations,
		PodAntiAffinityType: string(instancePlacementSpec.PlacementPolicy),
	}
}

func buildPostgresConfiguration(resource *addonsv1alpha1.PostgresCluster) cnpgv1.PostgresConfiguration {
	pgConfig := cnpgv1.PostgresConfiguration{}
	if resource.Spec.ReplicasSpec.NumSynchronousReplicas > 0 {
		pgConfig.Synchronous = &cnpgv1.SynchronousReplicaConfiguration{
			Method:         cnpgv1.SynchronousReplicaConfigurationMethodAny,
			Number:         resource.Spec.ReplicasSpec.NumSynchronousReplicas,
			DataDurability: resource.Spec.ReplicasSpec.SynchronousReplicaDataDurability,
		}
	}
	pgSpec := resource.Spec.PostgreSQLSpec.DeepCopy()
	if len(resource.Spec.PostgreSQLSpec.PostgresConf) != 0 {
		pgConfig.Parameters = pgSpec.PostgresConf
	}
	return pgConfig
}

func buildPluginsSpec(resource *addonsv1alpha1.PostgresCluster) []cnpgv1.PluginConfiguration {
	if resource.Spec.ClusterBackupSpec == nil {
		return nil
	}
	clusterBackupSpec := resource.Spec.ClusterBackupSpec
	if clusterBackupSpec.WalArchivingEnabled {
		// If WAL archiving is enabled, we need to add the barman-cloud plugin.
		return []cnpgv1.PluginConfiguration{
			{
				Name:          "barman-cloud.cloudnative-pg.io",
				Enabled:       ptr.To(true),
				IsWALArchiver: ptr.To(true),
				Parameters: map[string]string{
					"barmanObjectName": clusterBackupSpec.ObjectStoreName,
				},
			},
		}
	}
	return nil
}

func buildExternalClusters(resource *addonsv1alpha1.PostgresCluster) []cnpgv1.ExternalCluster {
	switch {
	case resource.Spec.BootstrapSpec == nil:
		return nil
	case resource.Spec.BootstrapSpec.InitDbSpec != nil && resource.Spec.BootstrapSpec.InitDbSpec.Import != nil:
		sourceClusterSpec := resource.Spec.BootstrapSpec.InitDbSpec.Import.SourceClusterSpec
		return []cnpgv1.ExternalCluster{
			{
				Name: sourceClusterSpec.Name,
				ConnectionParameters: map[string]string{
					"host":    sourceClusterSpec.Host,
					"port":    fmt.Sprintf("%d", sourceClusterSpec.Port),
					"user":    sourceClusterSpec.User,
					"dbname":  sourceClusterSpec.DbName,
					"sslmode": sourceClusterSpec.SslMode,
				},
				Password: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: sourceClusterSpec.Password.SecretRef.Name,
					},
					Key: sourceClusterSpec.Password.Key,
				},
			},
		}
	case resource.Spec.BootstrapSpec.RecoverySpec != nil && resource.Spec.BootstrapSpec.RecoverySpec.ObjectStoreSpec != nil:
		objectStoreSpec := resource.Spec.BootstrapSpec.RecoverySpec.ObjectStoreSpec
		return []cnpgv1.ExternalCluster{
			{
				Name: objectStoreSpec.ObjectStoreName,
				PluginConfiguration: &cnpgv1.PluginConfiguration{
					Name: "barman-cloud.cloudnative-pg.io",
					Parameters: map[string]string{
						"barmanObjectName": objectStoreSpec.ObjectStoreName,
						"serverName":       objectStoreSpec.SourceClusterName,
					},
				},
			},
		}
	}
	return nil
}

func buildBootstrapSpec(resource *addonsv1alpha1.PostgresCluster) *cnpgv1.BootstrapConfiguration {
	switch {
	case resource.Spec.BootstrapSpec == nil:
		return nil
	case *resource.Spec.BootstrapSpec == addonsv1alpha1.BootstrapSpec{}:
		// If the bootstrap spec is empty, we return nil.
		return nil
	case resource.Spec.BootstrapSpec.InitDbSpec != nil:
		initDbSpec := resource.Spec.BootstrapSpec.InitDbSpec
		res := &cnpgv1.BootstrapConfiguration{
			InitDB: &cnpgv1.BootstrapInitDB{},
		}
		if initDbSpec.Import != nil {
			res.InitDB.Import = &cnpgv1.Import{
				Source: cnpgv1.ImportSource{
					ExternalCluster: initDbSpec.Import.SourceClusterSpec.Name,
				},
				Type:      cnpgv1.MicroserviceSnapshotType,
				Databases: initDbSpec.Import.Databases,
			}
		}
		return res
	case resource.Spec.BootstrapSpec.RecoverySpec != nil && resource.Spec.BootstrapSpec.RecoverySpec.ObjectStoreSpec != nil:
		// We are bootstrapping from an object store
		recoverySpec := resource.Spec.BootstrapSpec.RecoverySpec
		res := buildRecoveryDefaults(recoverySpec)
		res.Recovery.Source = recoverySpec.ObjectStoreSpec.ObjectStoreName
		// If recovery target is specified, set it in the recovery spec. Ie PITR
		if recoverySpec.ObjectStoreSpec.RecoveryTarget != nil {
			res.Recovery.RecoveryTarget = &cnpgv1.RecoveryTarget{
				TargetTime: recoverySpec.ObjectStoreSpec.RecoveryTarget.RecoveryTargetTime,
			}
		}
		return res
	case resource.Spec.BootstrapSpec.RecoverySpec != nil && resource.Spec.BootstrapSpec.RecoverySpec.BackupSpec != nil:
		// We are bootstrapping from a backup
		recoverySpec := resource.Spec.BootstrapSpec.RecoverySpec
		res := buildRecoveryDefaults(recoverySpec)
		res.Recovery.Backup = &cnpgv1.BackupSource{
			LocalObjectReference: cnpgv1.LocalObjectReference{
				Name: recoverySpec.BackupSpec.BackupName,
			},
		}
		return res
	default:
		return nil
	}
}

func buildRecoveryDefaults(recovery *addonsv1alpha1.RecoverySpec) *cnpgv1.BootstrapConfiguration {
	if recovery == nil {
		return nil
	}

	res := &cnpgv1.BootstrapConfiguration{
		Recovery: &cnpgv1.BootstrapRecovery{},
	}

	return res
}

func (r *pgClusterReconciler) Name() string {
	return pgClusterReconcilerName
}
