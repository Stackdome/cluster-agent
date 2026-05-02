package stackstorage

import (
	"context"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"

	"stackdome.io/cluster-agent/internal/controller"
)

type volumeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *volumeReconciler) reconcile(ctx context.Context, storage *storagev1alpha1.Storage) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling volume")
	existingVolumes := make(map[string]*storagev1alpha1.Volume)
	for volumeName, volumeSpec := range storage.Spec.VolumeSpecs {
		reconcileRes, existingVolume, err := r.reconcileVolume(ctx, volumeName, volumeSpec, storage)
		switch {
		case err != nil:
			return resultNil, err
		case reconcileRes != resultNil:
			return reconcileRes, err
		default:
			existingVolumes[existingVolume.Name] = existingVolume
		}
	}

	if err := r.removeExtraVolumes(ctx, storage, definedVolumeNames(storage)); err != nil {
		return resultNil, err
	}

	setVolumeStatus(storage, existingVolumes)

	return resultNil, nil
}

func definedVolumeNames(storage *storagev1alpha1.Storage) map[string]struct{} {
	definedVolumeNames := make(map[string]struct{})
	for volumeName := range storage.Spec.VolumeSpecs {
		definedVolumeNames[string(volumeName)] = struct{}{}
	}
	return definedVolumeNames
}

func (r *volumeReconciler) removeExtraVolumes(
	ctx context.Context, storage *storagev1alpha1.Storage, definedVolumes map[string]struct{}) error {
	existingVolumes := &storagev1alpha1.VolumeList{}
	if err := r.Client.List(
		ctx, existingVolumes,
		client.InNamespace(storage.Namespace),
		client.MatchingLabels{storagev1alpha1.StorageLabel: storage.Name}); err != nil {
		return err
	}

	for _, existingWorkspaceVolume := range existingVolumes.Items {
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

func (r *volumeReconciler) reconcileVolume(
	ctx context.Context, volumeName storagev1alpha1.VolumeName, volumeSpec *storagev1alpha1.VolumeSpec, ws *storagev1alpha1.Storage) (subReconcilerResult, *storagev1alpha1.Volume, error) {
	labels := make(map[string]string)
	annotations := make(map[string]string)

	for key, value := range volumeSpec.Labels {
		labels[key] = value
	}
	// Add workspace storage label to the volume.
	labels[storagev1alpha1.StorageLabel] = ws.Name

	for key, value := range volumeSpec.Annotations {
		annotations[key] = value
	}

	desiredVolume := &storagev1alpha1.Volume{
		ObjectMeta: v1.ObjectMeta{
			Name:        string(volumeName),
			Namespace:   ws.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: storagev1alpha1.VolumeSpec{
			Size:               volumeSpec.Size,
			StorageClass:       volumeSpec.StorageClass,
			NeedsSyncBeforeUse: volumeSpec.NeedsSyncBeforeUse,
			Source:             volumeSpec.Source,
			AccessMode:         volumeSpec.AccessMode,
		},
	}

	desiredVolume.SetGroupVersionKind(storagev1alpha1.GroupVersion.WithKind("Volume"))

	if err := controllerutil.SetControllerReference(ws, desiredVolume, r.Scheme); err != nil {
		return resultNil, nil, err
	}

	existingVolume := &storagev1alpha1.Volume{}

	if err := r.Client.Get(ctx, controller.GetNamespacedName(desiredVolume), existingVolume); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, nil, r.Client.Create(ctx, desiredVolume)
		}
		return resultNil, nil, err
	}

	if !equality.Semantic.DeepEqual(existingVolume.Spec, desiredVolume.Spec) {
		desiredVolume.ResourceVersion = existingVolume.ResourceVersion
		if err := r.Client.Update(ctx, desiredVolume); err != nil {
			return resultNil, nil, err
		}
	}

	if volumeAvailable(existingVolume) {
		return resultNil, existingVolume, nil
	}
	// Volume not yet available.
	reportStorageUnAvailable(ws, "VolumeNotReady", "Volume '%s' not ready", desiredVolume.Name)
	return resultStop, existingVolume, nil
}

func volumeAvailable(existingVolume *storagev1alpha1.Volume) bool {
	availableCond := getVolumeAvailableCondition(existingVolume)
	if availableCond != nil && availableCond.Status == v1.ConditionTrue && availableCond.ObservedGeneration == existingVolume.Generation {
		return true
	}
	return false
}

func getVolumeAvailableCondition(existingVolume *storagev1alpha1.Volume) *v1.Condition {
	return meta.FindStatusCondition(existingVolume.Status.Conditions, string(storagev1alpha1.VolumeConditionAvailable))
}

func setVolumeStatus(storage *storagev1alpha1.Storage, existingVolumes map[string]*storagev1alpha1.Volume) {
	res := make([]storagev1alpha1.VolumeInfo, 0)
	for volumeName, volume := range existingVolumes {
		currentVolumeStatus := storagev1alpha1.VolumeInfo{
			VolumeName: volumeName,
			Subpath:    storage.MountPathForVolume(volumeName),
			Available:  volumeAvailable(volume),
		}
		res = append(res, currentVolumeStatus)
	}
	storage.Status.VolumeStatus = res
}
