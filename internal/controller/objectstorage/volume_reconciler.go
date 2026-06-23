package objectstorage

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

type volumeReconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

func newVolumeReconciler(c client.Client, scheme *runtime.Scheme) *volumeReconciler {
	return &volumeReconciler{client: c, scheme: scheme}
}

func (r *volumeReconciler) name() string { return "volume-reconciler" }

func (r *volumeReconciler) reconcile(ctx context.Context, resource *storagev1alpha1.ObjectStorage) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)

	desiredVolume := &storagev1alpha1.Volume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resource.VolumeName(),
			Namespace: resource.Namespace,
		},
		Spec: storagev1alpha1.VolumeSpec{
			Size:               resource.Spec.Capacity,
			NeedsSyncBeforeUse: false,
			AccessMode:         corev1.ReadWriteOnce,
		},
	}

	if resource.Spec.StorageClassName != nil {
		desiredVolume.Spec.StorageClass = *resource.Spec.StorageClassName
	}

	if err := controllerutil.SetControllerReference(resource, desiredVolume, r.scheme); err != nil {
		return resultNil, err
	}

	existingVolume := &storagev1alpha1.Volume{}
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(desiredVolume), existingVolume); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating Volume", "name", desiredVolume.Name)
			return resultRequeue, r.client.Create(ctx, desiredVolume)
		}
		return resultNil, err
	}

	volumeAvailable := meta.FindStatusCondition(existingVolume.Status.Conditions, string(storagev1alpha1.VolumeConditionAvailable))
	if volumeAvailable == nil || volumeAvailable.Status == metav1.ConditionFalse || volumeAvailable.ObservedGeneration != existingVolume.Generation {
		logger.Info("Volume not available", "name", existingVolume.Name)
		setStatusCondition(resource, storagev1alpha1.ObjectStorageConditionAvailable, metav1.ConditionFalse, "VolumeNotAvailable", "Backing volume is not available")
		setPhase(resource, storagev1alpha1.ObjectStoragePhasePending)
		return resultStop, nil
	}
	resource.Status.VolumeName = existingVolume.Name
	resource.Status.PVCName = existingVolume.Status.PvcName
	return resultNil, nil
}
