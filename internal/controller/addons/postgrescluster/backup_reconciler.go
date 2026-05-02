package postgrescluster

import (
	"context"
	"fmt"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8sapierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
)

type backupReconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

func newBackupReconciler(client client.Client, scheme *runtime.Scheme) *backupReconciler {
	return &backupReconciler{
		client: client,
		scheme: scheme,
	}
}

func (r *backupReconciler) name() string {
	return "backup-reconciler"
}

func (r *backupReconciler) reconcile(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error) {
	if resource.Spec.ClusterBackupSpec == nil {
		return resultNil, nil
	}

	logger := log.FromContext(ctx)
	logger.Info("Reconciling backups for PostgresCluster", "name", resource.Name, "namespace", resource.Namespace)

	readyCond := meta.FindStatusCondition(resource.Status.Conditions, string(addonsv1alpha1.ClusterReady))
	if readyCond == nil || readyCond.ObservedGeneration != resource.Generation || readyCond.Status != metav1.ConditionTrue {
		logger.Info("PostgresCluster is not ready, skipping backup reconciliation", "name", resource.Name, "namespace", resource.Namespace)
		return resultNil, nil
	}

	if err := r.reconcileScheduledBackup(ctx, resource); err != nil {
		logger.Error(err, "Failed to reconcile scheduled backup", "name", resource.Name, "namespace", resource.Namespace)
		return resultNil, err
	}

	cnpgCluster := &cnpgv1.Cluster{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Name:      resource.CnpgClusterName(),
		Namespace: resource.Namespace,
	}, cnpgCluster); err != nil {
		return resultNil, err
	}

	lastBackupSucceededCond := meta.FindStatusCondition(cnpgCluster.Status.Conditions, string(cnpgv1.ConditionBackup))
	if lastBackupSucceededCond != nil {
		// We suffix last transition time to let the status change each time the last transition time changes in the original cnpg condition.
		message := fmt.Sprintf("%s[%s]", lastBackupSucceededCond.Message, lastBackupSucceededCond.LastTransitionTime.UTC().Format(time.RFC3339))
		setStatusCondition(resource, addonsv1alpha1.LastBaseBackupSucceeded, lastBackupSucceededCond.Status, lastBackupSucceededCond.Reason, message)
	}

	if resource.Spec.ClusterBackupSpec.WalArchivingEnabled {
		continousArchivingCond := meta.FindStatusCondition(cnpgCluster.Status.Conditions, string(cnpgv1.ConditionContinuousArchiving))
		if continousArchivingCond != nil {
			setStatusCondition(resource, addonsv1alpha1.ContinuousWalArchivingSuccess, continousArchivingCond.Status, continousArchivingCond.Reason, continousArchivingCond.Message)
		}
	}

	if !shouldCreateImmeadiateBackup(resource) {
		return resultNil, nil
	}

	desiredBackup := &cnpgv1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resource.CnpgClusterName() + "-" + resource.Spec.ClusterBackupSpec.LastBaseBackupRequestedAt.UTC().Format("20060102-150405"),
			Namespace: resource.Namespace,
		},
		Spec: cnpgv1.BackupSpec{
			Cluster: cnpgv1.LocalObjectReference{
				Name: resource.CnpgClusterName(),
			},
			Method: cnpgv1.BackupMethodPlugin,
			PluginConfiguration: &cnpgv1.BackupPluginConfiguration{
				Name: "barman-cloud.cloudnative-pg.io",
				Parameters: map[string]string{
					"barmanObjectName": resource.Spec.ClusterBackupSpec.ObjectStoreName,
				},
			},
		},
	}
	desiredBackup.SetGroupVersionKind(cnpgv1.SchemeGroupVersion.WithKind("Backup"))
	if err := controllerutil.SetControllerReference(resource, desiredBackup, r.scheme); err != nil {
		return resultNil, err
	}

	if err := r.client.Create(ctx, desiredBackup); client.IgnoreAlreadyExists(err) != nil {
		return resultNil, err
	}

	// Mark this LastBaseBackupRequestedAt request as processed.
	resource.Status.LastImmeadiateBackupProcessedAt = &metav1.Time{
		Time: time.Now().UTC(),
	}

	return resultNil, nil
}

func (r *backupReconciler) reconcileScheduledBackup(
	ctx context.Context,
	resource *addonsv1alpha1.PostgresCluster,
) error {
	backupSpec := resource.Spec.ClusterBackupSpec
	if backupSpec.ScheduledBaseBackupSpec != nil && backupSpec.ScheduledBaseBackupSpec.Enabled {
		// If wal archiving is enabled, we need to create ScheduledBackup with immeadate set to true.
		desiredScheduledBackup := buildDesiredScheduledBackup(resource, backupSpec.WalArchivingEnabled)
		if err := controllerutil.SetControllerReference(resource, desiredScheduledBackup, r.scheme); err != nil {
			return err
		}
		existingScheduledBackup := cnpgv1.ScheduledBackup{}
		if err := r.client.Get(ctx, client.ObjectKey{
			Name:      scheduledBackupName(resource),
			Namespace: resource.Namespace,
		}, &existingScheduledBackup); err != nil {
			if k8sapierrors.IsNotFound(err) {
				return r.client.Create(ctx, desiredScheduledBackup)
			}
			return err
		}
		if !equality.Semantic.DeepEqual(existingScheduledBackup.Spec, desiredScheduledBackup.Spec) {
			desiredScheduledBackup.ResourceVersion = existingScheduledBackup.ResourceVersion
			if err := r.client.Update(ctx, desiredScheduledBackup); err != nil {
				return err
			}
		}
	}
	return nil
}

func shouldCreateImmeadiateBackup(
	postgresCluster *addonsv1alpha1.PostgresCluster,
) bool {
	if postgresCluster.Spec.ClusterBackupSpec.LastBaseBackupRequestedAt == nil {
		return false
	}
	// No last immediate backup processed, so we should create one
	if postgresCluster.Status.LastImmeadiateBackupProcessedAt == nil {
		return true
	}
	// If the last immediate backup processed is before the last base backup requested,
	// we should create a new immediate backup.
	return postgresCluster.Status.LastImmeadiateBackupProcessedAt.UTC().Before(
		postgresCluster.Spec.ClusterBackupSpec.LastBaseBackupRequestedAt.UTC(),
	)
}

func scheduledBackupName(postgresCluster *addonsv1alpha1.PostgresCluster) string {
	return postgresCluster.CnpgClusterName() + "-scheduled-backup"
}

func buildDesiredScheduledBackup(
	postgresCluster *addonsv1alpha1.PostgresCluster,
	immeadiate bool,
) *cnpgv1.ScheduledBackup {
	res := &cnpgv1.ScheduledBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scheduledBackupName(postgresCluster),
			Namespace: postgresCluster.Namespace,
		},
		Spec: cnpgv1.ScheduledBackupSpec{
			BackupOwnerReference: "cluster",
			Schedule:             postgresCluster.Spec.ClusterBackupSpec.ScheduledBaseBackupSpec.Schedule,
			Immediate:            ptr.To(immeadiate),
			Cluster: cnpgv1.LocalObjectReference{
				Name: postgresCluster.CnpgClusterName(),
			},
			Method: cnpgv1.BackupMethodPlugin,
			PluginConfiguration: &cnpgv1.BackupPluginConfiguration{
				Name: "barman-cloud.cloudnative-pg.io",
				Parameters: map[string]string{
					"barmanObjectName": postgresCluster.Spec.ClusterBackupSpec.ObjectStoreName,
				},
			},
		},
	}
	res.SetGroupVersionKind(cnpgv1.SchemeGroupVersion.WithKind("ScheduledBackup"))
	return res
}
