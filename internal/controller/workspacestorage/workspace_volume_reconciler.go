package workspacestorage

import (
	"context"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"soradev.io/cluster-agent/api/v1alpha1"
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

type workspaceVolumeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *workspaceVolumeReconciler) reconcile(ctx context.Context, workspaceStorage *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling pvc")
	existingVolumes := make(map[string]*workspacev1alpha1.WorkspaceVolume)
	for i := range workspaceStorage.Spec.ResourceStorageSpecs {
		currentResource := &workspaceStorage.Spec.ResourceStorageSpecs[i]
		reconcileRes, existingVolume, err := r.reconcileWorkspaceVolume(ctx, currentResource, workspaceStorage)
		switch {
		case err != nil:
			return resultNil, err
		case reconcileRes != resultNil:
			return reconcileRes, err
		default:
			existingVolumes[existingVolume.Name] = existingVolume
		}
	}
	setWorkspaceVolumeStatus(workspaceStorage, existingVolumes)
	return resultNil, nil
}

// TODO: Remove any extra volumes.
func (r *workspaceVolumeReconciler) reconcileWorkspaceVolume(
	ctx context.Context, volumeSpec *workspacev1alpha1.ResourceStorageSpec, ws *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, *workspacev1alpha1.WorkspaceVolume, error) {
	desiredWorkspaceVolume := &workspacev1alpha1.WorkspaceVolume{
		ObjectMeta: v1.ObjectMeta{
			Name:      volumeSpec.VolumeName,
			Namespace: ws.Namespace,
			Labels: map[string]string{
				workspacev1alpha1.WorkspaceStorageVolumeLabel: ws.Name,
			},
		},
		Spec: workspacev1alpha1.WorkspaceVolumeSpec{
			Size:               volumeSpec.Size,
			Type:               volumeSpec.Type,
			NeedsSyncBeforeUse: volumeSpec.NeedsSyncBeforeUse,
			DontAllowSync:      volumeSpec.DontAllowSync,
		},
	}

	if err := controllerutil.SetControllerReference(ws, desiredWorkspaceVolume, r.Scheme); err != nil {
		return resultNil, nil, err
	}

	existingWSVolume := &workspacev1alpha1.WorkspaceVolume{}

	if err := r.Client.Get(ctx, controller.GetNamespacedName(desiredWorkspaceVolume), existingWSVolume); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, nil, r.Client.Create(ctx, desiredWorkspaceVolume)
		}
		return resultNil, nil, err
	}

	if !equality.Semantic.DeepDerivative(desiredWorkspaceVolume.Spec, existingWSVolume.Spec) {
		existingWSVolume.Spec = desiredWorkspaceVolume.Spec
		return resultRequeue, existingWSVolume, r.Client.Update(ctx, existingWSVolume)
	}

	if workspaceVolumeAvailable(existingWSVolume) {
		return resultNil, existingWSVolume, nil
	}
	// Volume not yet available.
	reportWorkspaceStorageUnAvailable(ws, "WorkspaceVolumeNotReady", "WorkspaceVolume '%s' not ready", desiredWorkspaceVolume.Name)
	return resultStop, existingWSVolume, nil
}

func workspaceVolumeAvailable(existingWSVolume *workspacev1alpha1.WorkspaceVolume) bool {
	availableCond := getWorkspaceVolumeAvailableCondition(existingWSVolume)
	if availableCond != nil && availableCond.Status == v1.ConditionTrue && availableCond.ObservedGeneration == existingWSVolume.Generation {
		return true
	}
	return false
}

func getWorkspaceVolumeAvailableCondition(existingWSVolume *workspacev1alpha1.WorkspaceVolume) *v1.Condition {
	return meta.FindStatusCondition(existingWSVolume.Status.Conditions, string(workspacev1alpha1.WorkspaceVolumeConditionAvailable))
}

func setWorkspaceVolumeStatus(workspaceStorage *workspacev1alpha1.WorkspaceStorage, existingVolumes map[string]*workspacev1alpha1.WorkspaceVolume) {
	res := make([]v1alpha1.VolumeStatus, 0)
	for volumeName, volume := range existingVolumes {
		currentVolumeStatus := v1alpha1.VolumeStatus{
			VolumeName:   volumeName,
			VolumeType:   volume.Spec.Type,
			Subpath:      workspaceStorage.MountPathForVolume(volumeName),
			Available:    workspaceVolumeAvailable(volume),
			LastSyncedAt: volume.Status.LastSyncedAt,
		}
		res = append(res, currentVolumeStatus)
	}
	workspaceStorage.Status.WorkspaceVolumeStatus = res
}
