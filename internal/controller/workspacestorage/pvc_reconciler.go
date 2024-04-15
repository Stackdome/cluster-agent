package workspacestorage

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

type pvcReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *pvcReconciler) reconcile(ctx context.Context, workspaceStorage *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling pvc")
	for i := range workspaceStorage.Spec.ResourceStorageSpecs {
		currentResource := &workspaceStorage.Spec.ResourceStorageSpecs[i]
		if err := r.ensurePVC(ctx, currentResource, workspaceStorage); err != nil {
			return resultNil, err
		}
	}
	return resultNil, nil
}

func (r *pvcReconciler) ensurePVC(ctx context.Context, resource *workspacev1alpha1.ResourceStorageSpec, workplaceStorage *workspacev1alpha1.WorkspaceStorage) error {
	// TODO, change this based on the type.
	resourceSize, err := k8sresource.ParseQuantity(resource.Size)
	if err != nil {
		return fmt.Errorf("failed to parse resource size in the resource: %w", err)
	}

	desiredPVC := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workplaceStorage.GeneratePVCName(resource),
			Namespace: workplaceStorage.Namespace,
			Labels:    WorkspaceStorageLabels(workplaceStorage),
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

	if err := controllerutil.SetOwnerReference(workplaceStorage, &desiredPVC, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner refs: %w", err)
	}

	existingPVC := &corev1.PersistentVolumeClaim{}

	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      desiredPVC.Name,
		Namespace: desiredPVC.Namespace,
	}, existingPVC); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Client.Create(ctx, &desiredPVC)
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
