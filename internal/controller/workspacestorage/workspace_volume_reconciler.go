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
	for i := range workspaceStorage.Spec.ResourceStorageSpecs {
		currentResource := &workspaceStorage.Spec.ResourceStorageSpecs[i]
		if reconcileRes, err := r.reconcileWorkspaceVolume(ctx, currentResource, workspaceStorage); err != nil {
			return resultNil, err
		} else if reconcileRes != resultNil {
			return reconcileRes, err
		}
	}
	return resultNil, nil
}

// TODO: Remove any extra volumes.
func (r *workspaceVolumeReconciler) reconcileWorkspaceVolume(
	ctx context.Context, volumeSpec *workspacev1alpha1.ResourceStorageSpec, ws *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, error) {
	desiredWorkspaceVolume := &workspacev1alpha1.WorkspaceVolume{
		ObjectMeta: v1.ObjectMeta{
			Name:      volumeSpec.VolumeName,
			Namespace: ws.Namespace,
		},
		Spec: workspacev1alpha1.WorkspaceVolumeSpec{
			Size:          volumeSpec.Size,
			Type:          volumeSpec.Type,
			NeedsSync:     volumeSpec.NeedsSync,
			DontAllowSync: volumeSpec.DontAllowSync,
		},
	}

	if err := controllerutil.SetControllerReference(ws, desiredWorkspaceVolume, r.Scheme); err != nil {
		return resultNil, err
	}

	existingWSVolume := &workspacev1alpha1.WorkspaceVolume{}

	if err := r.Client.Get(ctx, controller.GetNamespacedName(desiredWorkspaceVolume), existingWSVolume); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, desiredWorkspaceVolume)
		}
		return resultNil, err
	}

	if !equality.Semantic.DeepDerivative(desiredWorkspaceVolume.Spec, existingWSVolume.Spec) {
		existingWSVolume.Spec = desiredWorkspaceVolume.Spec
		return resultRequeue, r.Client.Update(ctx, existingWSVolume)
	}

	availableCond := meta.FindStatusCondition(existingWSVolume.Status.Conditions, string(workspacev1alpha1.WorkspaceVolumeConditionAvailable))
	if availableCond != nil && availableCond.Status == v1.ConditionTrue && availableCond.ObservedGeneration == existingWSVolume.Generation {
		return resultNil, nil
	}
	// Volume not yet available.
	reportWorkspaceStorageUnAvailable(ws, "WorkspaceVolumeNotReady", "WorkspaceVolume '%s' not ready", desiredWorkspaceVolume.Name)
	return resultStop, nil
}
