package postgrescluster

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	barmancloudv1 "github.com/cloudnative-pg/plugin-barman-cloud/api/v1"
	gocron "github.com/robfig/cron/v3"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8sapierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
	"stackdome.io/cluster-agent/pkg/database"
)

const (
	requeueIntervalOnValidationFailure = 30 * time.Second
)

type dbConnectionChecker interface {
	Ping(connectionString string) error
}

type resourceValidationReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	dbConnectionChecker dbConnectionChecker
}

func newResourceValidationReconciler(client client.Client, scheme *runtime.Scheme) *resourceValidationReconciler {
	return &resourceValidationReconciler{
		Client:              client,
		Scheme:              scheme,
		dbConnectionChecker: database.NewDatabaseConnection(database.PostgresDialect),
	}
}

func (r *resourceValidationReconciler) name() string {
	return "resource-validation-reconciler"
}

func (r *resourceValidationReconciler) reconcile(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error) {
	if !r.shouldRunValidation(resource) {
		return resultNil, nil
	}

	validations := map[string]func(context.Context, *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error){
		"ReplicasSpecValidation":  r.validateReplicasSpec,
		"StorageSpecValidation":   r.validateStorageSpec,
		"BackupSpecValidation":    r.validateBackupSpec,
		"ImageCatalogValidation":  r.validateImageCatalogPresent,
		"BootstrapSpecValidation": r.validateBootstrapSpec,
	}
	for validationName, validationFunc := range validations {
		result, err := validationFunc(ctx, resource)
		switch {
		case err != nil:
			return resultNil, fmt.Errorf("error during %s: %w", validationName, err)
		case result.resultStop, result.resultRequeue:
			// We need to requeue on stop or requeue
			return resultRequeueAfter(requeueIntervalOnValidationFailure), nil
		case result.resultRequeueAfter != nil:
			return resultRequeueAfter(*result.resultRequeueAfter), nil
		}
	}
	setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionTrue, "ClusterConfigurationValid", "PostgresCluster configuration is valid")
	return resultNil, nil
}

func (r *resourceValidationReconciler) validateReplicasSpec(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error) {
	if resource.Spec.ReplicasSpec.NumSynchronousReplicas >= resource.Spec.Instances {
		setStatusCondition(
			resource,
			addonsv1alpha1.ClusterConfigurationValid,
			metav1.ConditionFalse,
			"InvalidSynchronousReplicas",
			fmt.Sprintf("Number of synchronous replicas (%d) cannot be greater than or equal to total instances (%d)", resource.Spec.ReplicasSpec.NumSynchronousReplicas, resource.Spec.Instances),
		)
		setPhase(resource, addonsv1alpha1.ErrorPhase)
		return resultStop, nil
	}
	return resultNil, nil
}

func (r *resourceValidationReconciler) shouldRunValidation(resource *addonsv1alpha1.PostgresCluster) bool {
	configValidCond := meta.FindStatusCondition(resource.Status.Conditions, string(addonsv1alpha1.ClusterConfigurationValid))
	if configValidCond == nil || configValidCond.Status != metav1.ConditionTrue || configValidCond.ObservedGeneration != resource.Generation {
		return true
	}
	return false
}

func (r *resourceValidationReconciler) validateStorageSpec(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error) {
	storageClass := storagev1.StorageClass{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Name: resource.Spec.StorageSpec.StorageClassName,
	}, &storageClass)
	if err != nil {
		if k8sapierrors.IsNotFound(err) {
			setStatusCondition(
				resource,
				addonsv1alpha1.ClusterConfigurationValid,
				metav1.ConditionFalse,
				"StorageClassNotFound",
				fmt.Sprintf("storage class '%s' not found in cluster", resource.Spec.StorageSpec.StorageClassName),
			)
			setPhase(resource, addonsv1alpha1.ErrorPhase)
			return resultStop, nil
		}
		return resultNil, err
	}

	return resultNil, nil
}

func (r *resourceValidationReconciler) validateBackupSpec(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error) {
	if resource.Spec.ClusterBackupSpec == nil {
		return resultNil, nil
	}

	backupSpec := resource.Spec.ClusterBackupSpec

	backupObjectStore := &barmancloudv1.ObjectStore{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Name:      backupSpec.ObjectStoreName,
		Namespace: resource.Namespace,
	}, backupObjectStore)
	if err != nil {
		if k8sapierrors.IsNotFound(err) {
			setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "ObjectStoreNotFound", "Backup object store not found")
			setPhase(resource, addonsv1alpha1.ErrorPhase)
			return resultStop, nil
		}
		return resultNil, err
	}

	if backupSpec.WalArchivingEnabled {
		if backupSpec.ScheduledBaseBackupSpec == nil || !backupSpec.ScheduledBaseBackupSpec.Enabled {
			setStatusCondition(
				resource,
				addonsv1alpha1.ClusterConfigurationValid,
				metav1.ConditionFalse,
				"WalArchivingRequiresScheduledBackup",
				"WAL archiving requires scheduled base backup to be enabled",
			)
			setPhase(resource, addonsv1alpha1.ErrorPhase)
			return resultStop, nil
		}
	}
	if backupSpec.ScheduledBaseBackupSpec != nil && backupSpec.ScheduledBaseBackupSpec.Enabled {
		parser := gocron.NewParser(gocron.Second | gocron.Minute | gocron.Hour | gocron.Dom | gocron.Month | gocron.Dow)
		if _, err := parser.Parse(backupSpec.ScheduledBaseBackupSpec.Schedule); err != nil {
			setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "BaseBackupScheduleInvalid", "Base backup schedule is invalid")
			setPhase(resource, addonsv1alpha1.ErrorPhase)
			return resultStop, nil
		}
	}
	return resultNil, nil
}

func (r *resourceValidationReconciler) validateImageCatalogPresent(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error) {
	postgresqlSpec := resource.Spec.PostgreSQLSpec
	if postgresqlSpec.ImageCatalogRef == nil {
		setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "ImageCatalogNotFound", "PostgreSQL image catalog reference is not set")
		setPhase(resource, addonsv1alpha1.ErrorPhase)
		return resultStop, nil
	}

	if postgresqlSpec.ImageCatalogRef.Name == "" {
		setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "ImageCatalogNameEmpty", "PostgreSQL image catalog name is empty")
		setPhase(resource, addonsv1alpha1.ErrorPhase)
		return resultStop, nil
	}

	imageCatalog := &cnpgv1.ImageCatalog{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Name:      postgresqlSpec.ImageCatalogRef.Name,
		Namespace: resource.Namespace,
	}, imageCatalog)
	if err != nil {
		if k8sapierrors.IsNotFound(err) {
			setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "ImageCatalogNotFound", "PostgreSQL image catalog not found")
			setPhase(resource, addonsv1alpha1.ErrorPhase)
			return resultStop, nil
		}
		return resultNil, err
	}

	_, exists := lo.Find(imageCatalog.Spec.Images, func(imagectlg cnpgv1.CatalogImage) bool {
		return imagectlg.Major == postgresqlSpec.PostgreSQLMajorVersion
	})
	if !exists {
		setStatusCondition(
			resource,
			addonsv1alpha1.ClusterConfigurationValid,
			metav1.ConditionFalse,
			"ImageCatalogVersionNotFound",
			fmt.Sprintf("PostgreSQL version '%d' not found in image catalog '%s'", postgresqlSpec.PostgreSQLMajorVersion, postgresqlSpec.ImageCatalogRef.Name),
		)
		setPhase(resource, addonsv1alpha1.ErrorPhase)
		return resultStop, nil
	}

	return resultNil, nil
}

func (r *resourceValidationReconciler) validateBootstrapSpec(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error) {
	if resource.Spec.BootstrapSpec == nil {
		return resultNil, nil
	}

	bootstrapSpec := resource.Spec.BootstrapSpec

	if bootstrapSpec.InitDbSpec != nil {
		return r.validateInitDbSpec(ctx, resource, bootstrapSpec.InitDbSpec)
	}

	if bootstrapSpec.RecoverySpec != nil {
		return r.validateRecoverySpec(ctx, resource, bootstrapSpec.RecoverySpec)
	}

	return resultNil, nil
}

func (r *resourceValidationReconciler) validateRecoverySpec(ctx context.Context, resource *addonsv1alpha1.PostgresCluster, recoverySpec *addonsv1alpha1.RecoverySpec) (subReconcilerResult, error) {
	if recoverySpec.ObjectStoreSpec != nil {
		objectStoreSpec := recoverySpec.ObjectStoreSpec
		objectStore := &barmancloudv1.ObjectStore{}
		err := r.Client.Get(ctx, client.ObjectKey{
			Name:      recoverySpec.ObjectStoreSpec.ObjectStoreName,
			Namespace: resource.Namespace,
		}, objectStore)
		if err != nil {
			if k8sapierrors.IsNotFound(err) {
				setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "ObjectStoreNotFound", "Object store not found for recovery")
				setPhase(resource, addonsv1alpha1.ErrorPhase)
				return resultStop, nil
			}
			return resultNil, err
		}
		existingBackupsInObjectStoreMap := objectStore.Status.ServerRecoveryWindow
		_, found := existingBackupsInObjectStoreMap[objectStoreSpec.SourceClusterName]
		if !found {
			setStatusCondition(
				resource,
				addonsv1alpha1.ClusterConfigurationValid,
				metav1.ConditionFalse,
				"ObjectStoreNoBackupsFound",
				"No backups found for source cluster '"+objectStoreSpec.SourceClusterName+"' in object store '"+objectStoreSpec.ObjectStoreName+"'")
			setPhase(resource, addonsv1alpha1.ErrorPhase)
			return resultStop, nil
		}
	}
	if recoverySpec.BackupSpec != nil {
		backupObject := cnpgv1.Backup{}
		err := r.Client.Get(ctx, client.ObjectKey{
			Name:      recoverySpec.BackupSpec.BackupName,
			Namespace: resource.Namespace,
		}, &backupObject)
		if err != nil {
			if k8sapierrors.IsNotFound(err) {
				setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "BackupNotFound", "Backup not found for recovery")
				setPhase(resource, addonsv1alpha1.ErrorPhase)
				return resultStop, nil
			}
			return resultNil, err
		}
		if backupObject.Status.Phase != cnpgv1.BackupPhaseCompleted {
			setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "BackupNotCompleted", "Referenced Backup is not completed for recovery")
			setPhase(resource, addonsv1alpha1.ErrorPhase)
			return resultStop, nil
		}
	}
	return resultNil, nil
}

func (r *resourceValidationReconciler) validateInitDbSpec(ctx context.Context, resource *addonsv1alpha1.PostgresCluster, initDbSpec *addonsv1alpha1.InitDbSpec) (subReconcilerResult, error) {
	// validate importSpec
	if initDbSpec.Import != nil {
		return r.ensureSourceClusterAccess(ctx, resource, initDbSpec.Import.SourceClusterSpec)
	}
	return resultNil, nil
}

func (r *resourceValidationReconciler) ensureSourceClusterAccess(ctx context.Context, resource *addonsv1alpha1.PostgresCluster, sourceClusterSpec *addonsv1alpha1.ExternalClusterSpec) (subReconcilerResult, error) {
	sourceClusterPasswordSecret, err := r.getSecretFromNamespace(ctx, resource.Namespace, sourceClusterSpec.Password.SecretRef.Name)
	if err != nil {
		if k8sapierrors.IsNotFound(err) {
			setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "InitDbSourceClusterPasswordSecretNotFound", "Source cluster password secret not found")
			setPhase(resource, addonsv1alpha1.ErrorPhase)
			return resultStop, nil
		}
		return resultNil, err
	}
	sourceClusterPassword := sourceClusterPasswordSecret.Data[sourceClusterSpec.Password.Key]
	base64DecodedPassword, err := base64.StdEncoding.DecodeString(string(sourceClusterPassword))
	if err != nil {
		setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "InitDbSourceClusterPasswordBase64DecodeError", "Failed to decode base64 encoded source cluster password")
		setPhase(resource, addonsv1alpha1.ErrorPhase)
		return resultStop, nil
	}
	if len(base64DecodedPassword) == 0 {
		setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "InitDbSourceClusterPasswordEmpty", "Source cluster password is empty")
		setPhase(resource, addonsv1alpha1.ErrorPhase)
		return resultStop, nil
	}

	connectionString := sourceClusterSpec.DbConnectionString(string(base64DecodedPassword))
	if err := r.dbConnectionChecker.Ping(connectionString); err != nil {
		setStatusCondition(resource, addonsv1alpha1.ClusterConfigurationValid, metav1.ConditionFalse, "InitDbSourceClusterConnectionError", "Failed to connect to source cluster")
		setPhase(resource, addonsv1alpha1.ErrorPhase)
		return resultStop, nil
	}
	return resultNil, nil
}

func (r *resourceValidationReconciler) getSecretFromNamespace(ctx context.Context, namespace, secretName string) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: namespace,
	}, secret)
	if err != nil {
		return nil, err
	}
	return secret, nil
}

func setStatusCondition(resource *addonsv1alpha1.PostgresCluster, conditionType addonsv1alpha1.PostgresClusterConditionType, status metav1.ConditionStatus, reason, message string) {
	resource.Status.ObservedGeneration = resource.Generation
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(conditionType),
		Status:             status,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func setPhase(resource *addonsv1alpha1.PostgresCluster, phase string) {
	resource.Status.Phase = phase
}
