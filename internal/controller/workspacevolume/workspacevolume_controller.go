package workspacevolume

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

// WorkspaceVolumeReconciler reconciles a WorkspaceVolume object
type WorkspaceVolumeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *WorkspaceVolumeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger = logger.WithValues("workspacevolume", req.NamespacedName.String())
	ctx = controller.ContextWithLogger(ctx, logger)
	volume := &v1alpha1.WorkspaceVolume{}

	if err := r.Client.Get(ctx, req.NamespacedName, volume); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	res, err := r.reconcile(ctx, volume)
	if err != nil {
		return ctrl.Result{}, err
	}

	return res, r.Status().Update(ctx, volume)
}

func (r *WorkspaceVolumeReconciler) reconcile(ctx context.Context, volume *v1alpha1.WorkspaceVolume) (ctrl.Result, error) {
	if err := r.ensurePVC(ctx, volume); err != nil {
		return ctrl.Result{}, err
	}
	reportWorkspaceVolumeAvailable(volume)

	if volume.Annotations != nil {
		syncedAt, syncedOnce := volume.Annotations[v1alpha1.LastSyncedAtAnnotation]
		if syncedOnce {
			reportWorkspaceVolumeSyncedOnce(volume, syncedAt)
			return ctrl.Result{}, nil
		}
	}
	reportWorkspaceVolumeNotSynced(volume)
	return ctrl.Result{}, nil
}

func (r *WorkspaceVolumeReconciler) ensurePVC(ctx context.Context, volume *v1alpha1.WorkspaceVolume) error {
	// TODO, change this based on the type.
	resourceSize, err := k8sresource.ParseQuantity(volume.Spec.Size)
	if err != nil {
		return fmt.Errorf("failed to parse resource size in the resource: %w", err)
	}

	desiredPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      volume.Name,
			Namespace: volume.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resourceSize},
			},
			// TODO:
			// Hardcode to local path for now
			StorageClassName: ptr.To("local-path"),
		},
	}

	if err := controllerutil.SetControllerReference(volume, desiredPVC, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner refs: %w", err)
	}

	existingPVC := &corev1.PersistentVolumeClaim{}

	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      desiredPVC.Name,
		Namespace: desiredPVC.Namespace,
	}, existingPVC); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Client.Create(ctx, desiredPVC)
		}
		return err
	}

	// TODO: check:
	// - desired object.spec == existing object.spec
	// - owner refs match
	// - PVC status to make sure they are ready.
	// - existingPVC.Status.Conditions to check if its ready, only proceed further object reconcilation
	// 	 if the pvc/storage is ready.
	return nil
}

func reportWorkspaceVolumeAvailable(volume *v1alpha1.WorkspaceVolume) {
	volume.Status.ObservedGeneration = volume.Generation
	volume.Status.Phase = v1alpha1.WorkspaceVolumePhaseReady
	volume.Status.PvcName = volume.Name
	meta.SetStatusCondition(&volume.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceVolumeConditionAvailable),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: volume.Generation,
		Reason:             "WorkspaceVolumeProvisioned",
		Message:            "Workspace Volume is provisioned.",
	})
}

func reportWorkspaceVolumeSyncedOnce(volume *v1alpha1.WorkspaceVolume, lastSyncedAt string) {
	syncedOnceCond := meta.FindStatusCondition(volume.Status.Conditions, string(v1alpha1.WorkspaceVolumeConditionSyncedOnce))
	if syncedOnceCond != nil && syncedOnceCond.Status == metav1.ConditionTrue {
		return
	}
	volume.Status.ObservedGeneration = volume.Generation
	parsedTime, err := time.Parse(time.RFC3339, lastSyncedAt)
	if err != nil {
		parsedTime = time.Now().UTC()
	}
	volume.Status.LastSyncedAt = ptr.To(metav1.NewTime(parsedTime.UTC()))
	meta.SetStatusCondition(&volume.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceVolumeConditionSyncedOnce),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: volume.Generation,
		Reason:             "WorkspaceVolumeSyncedOnce",
		Message:            "Workspace Volume has been synced atleast once.",
	})
}

func reportWorkspaceVolumeNotSynced(volume *v1alpha1.WorkspaceVolume) {
	syncedOnceCond := meta.FindStatusCondition(volume.Status.Conditions, string(v1alpha1.WorkspaceVolumeConditionSyncedOnce))
	if syncedOnceCond != nil && syncedOnceCond.Status == metav1.ConditionFalse {
		return
	}
	volume.Status.ObservedGeneration = volume.Generation
	meta.SetStatusCondition(&volume.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceVolumeConditionSyncedOnce),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: volume.Generation,
		Reason:             "WorkspaceVolumeNotSynced",
		Message:            "Workspace Volume has not been synced.",
	})
}

func reportWorkspaceVolumeNotReady(volume *v1alpha1.WorkspaceVolume, reason string, msg string, mgsArgs ...any) {
	volume.Status.ObservedGeneration = volume.Generation
	volume.Status.Phase = v1alpha1.WorkspaceVolumePhasePending
	meta.SetStatusCondition(&volume.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceVolumeConditionAvailable),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: volume.Generation,
		Reason:             reason,
		Message:            fmt.Sprintf(msg, mgsArgs...),
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceVolumeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.WorkspaceVolume{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Complete(r)
}
