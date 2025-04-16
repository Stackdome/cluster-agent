package workspacestorage

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	workspacev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

type workspaceVolumeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *workspaceVolumeReconciler) reconcile(ctx context.Context, workspaceStorage *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling workspacevolume")
	existingVolumes := make(map[string]*workspacev1alpha1.WorkspaceVolume)
	for volumeName, volumeSpec := range workspaceStorage.Spec.ResourceStorageSpecs {
		reconcileRes, existingVolume, err := r.reconcileWorkspaceVolume(ctx, volumeName, volumeSpec, workspaceStorage)
		switch {
		case err != nil:
			return resultNil, err
		case reconcileRes != resultNil:
			return reconcileRes, err
		default:
			existingVolumes[existingVolume.Name] = existingVolume
		}
	}

	if err := r.removeExtraWorkspaceVolumes(ctx, workspaceStorage, definedVolumeNames(workspaceStorage)); err != nil {
		return resultNil, err
	}

	setWorkspaceVolumeStatus(workspaceStorage, existingVolumes)

	return resultNil, nil
}

func definedVolumeNames(workspaceStorage *workspacev1alpha1.WorkspaceStorage) map[string]struct{} {
	definedVolumeNames := make(map[string]struct{})
	for volumeName := range workspaceStorage.Spec.ResourceStorageSpecs {
		definedVolumeNames[string(volumeName)] = struct{}{}
	}
	return definedVolumeNames
}

func (r *workspaceVolumeReconciler) removeExtraWorkspaceVolumes(
	ctx context.Context, ws *workspacev1alpha1.WorkspaceStorage, definedVolumes map[string]struct{}) error {
	existingWorkspaceVolumes := &workspacev1alpha1.WorkspaceVolumeList{}
	if err := r.Client.List(
		ctx, existingWorkspaceVolumes,
		client.InNamespace(ws.Namespace),
		client.MatchingLabels{workspacev1alpha1.WorkspaceStorageLabel: ws.Name}); err != nil {
		return err
	}

	for _, existingWorkspaceVolume := range existingWorkspaceVolumes.Items {
		if _, ok := definedVolumes[existingWorkspaceVolume.Name]; !ok {
			if err := r.Client.Delete(
				ctx,
				&existingWorkspaceVolume,
				&client.DeleteOptions{PropagationPolicy: ptr.To(v1.DeletePropagationBackground)}); err != nil {
				return err
			}
		}
	}
	return nil
}

// TODO: Remove any extra volumes.
func (r *workspaceVolumeReconciler) reconcileWorkspaceVolume(
	ctx context.Context, volumeName workspacev1alpha1.VolumeName, volumeSpec *workspacev1alpha1.WorkspaceVolumeSpec, ws *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, *workspacev1alpha1.WorkspaceVolume, error) {
	labels := make(map[string]string)
	annotations := make(map[string]string)

	for key, value := range volumeSpec.Labels {
		labels[key] = value
	}
	// Add workspace storage label to the volume.
	labels[workspacev1alpha1.WorkspaceStorageLabel] = ws.Name

	for key, value := range volumeSpec.Annotations {
		annotations[key] = value
	}

	desiredWorkspaceVolume := &workspacev1alpha1.WorkspaceVolume{
		ObjectMeta: v1.ObjectMeta{
			Name:        string(volumeName),
			Namespace:   ws.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: workspacev1alpha1.WorkspaceVolumeSpec{
			Size:               volumeSpec.Size,
			StorageClass:       volumeSpec.StorageClass,
			NeedsSyncBeforeUse: volumeSpec.NeedsSyncBeforeUse,
			Source:             volumeSpec.Source,
			AccessMode:         volumeSpec.AccessMode,
		},
	}

	desiredWorkspaceVolume.SetGroupVersionKind(workspacev1alpha1.GroupVersion.WithKind("WorkspaceVolume"))

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

	if err := r.Client.Patch(ctx, desiredWorkspaceVolume, client.Apply, &client.PatchOptions{
		Force:        ptr.To(true),
		FieldManager: WorkspaceStorageControllerName,
	}); err != nil {
		return resultNil, nil, err
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
			Subpath:      workspaceStorage.MountPathForVolume(volumeName),
			Available:    workspaceVolumeAvailable(volume),
			LastSyncedAt: volume.Status.LastSyncedAt,
		}
		res = append(res, currentVolumeStatus)
	}
	workspaceStorage.Status.WorkspaceVolumeStatus = res
}
