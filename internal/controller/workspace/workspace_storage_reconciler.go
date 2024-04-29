package workspace

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

func (r *WorkspaceReconciler) ReconcileWorkspaceStorage(ctx context.Context, workspace *v1alpha1.Workspace) (subReconcilerResult, error) {
	resourceStorageSpecs := make([]v1alpha1.ResourceStorageSpec, 0)
	logger := controller.LoggerFromContext(ctx)
	for _, resource := range workspace.Spec.Resources {
		currentResourceStorageSpec := &v1alpha1.ResourceStorageSpec{
			Name:      resource.Name,
			Size:      resource.StorageSize,
			NeedsSync: resource.SyncRequired,
		}
		if resource.Spec.ApplicationSourceSpec != nil {
			currentResourceStorageSpec.Type = v1alpha1.ApplicationSourceStorage
		}
		if resource.Spec.PrebuiltApplicationSpec != nil {
			currentResourceStorageSpec.Type = v1alpha1.PreBuiltApplicationStateStorage
			currentResourceStorageSpec.DontAllowSync = true
		}
		resourceStorageSpecs = append(resourceStorageSpecs, *currentResourceStorageSpec)
	}

	desiredWorkspaceStorageCR := &v1alpha1.WorkspaceStorage{
		ObjectMeta: v1.ObjectMeta{
			Name:      v1alpha1.WorkspaceStorageName(workspace.Spec.UserName),
			Namespace: workspace.Namespace,
		},
		Spec: v1alpha1.WorkspaceStorageSpec{
			ResourceStorageSpecs: resourceStorageSpecs,
		},
	}

	if err := controllerutil.SetControllerReference(workspace, desiredWorkspaceStorageCR, r.Scheme); err != nil {
		return resultNil, err
	}

	existingWorkspaceStorage, err := r.GetWorkspaceStorage(ctx, workspace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return resultStop, r.Client.Create(ctx, desiredWorkspaceStorageCR)
		}
		return resultNil, err
	}

	// TODO: Make sure that the underlying persistant volume allows resizing.
	if !equality.Semantic.DeepDerivative(desiredWorkspaceStorageCR.Spec, existingWorkspaceStorage.Spec) {
		existingWorkspaceStorage.Spec = desiredWorkspaceStorageCR.Spec
		logger.Info(fmt.Sprintf("ws diff, updating: %+v", cmp.Diff(existingWorkspaceStorage.Spec, desiredWorkspaceStorageCR.Spec)))
		logger.Info(fmt.Sprintf("workspace: %+v", workspace.Spec))

		return resultRequeue, r.Client.Update(ctx, existingWorkspaceStorage)
	}

	availableCond := meta.FindStatusCondition(existingWorkspaceStorage.Status.Conditions, string(v1alpha1.WorkspaceStorageAvailable))
	if availableCond != nil && availableCond.Status == v1.ConditionTrue {
		return resultNil, nil
	}
	reportWorkspaceNotReady(workspace, "WorkspaceStorageNotReady", "Storage for the workspace not yet ready.")
	return resultStop, nil
}

func (r *WorkspaceReconciler) GetWorkspaceStorage(ctx context.Context, workspace *v1alpha1.Workspace) (*v1alpha1.WorkspaceStorage, error) {
	workspaceStorage := &v1alpha1.WorkspaceStorage{}
	if err := r.Client.Get(
		ctx,
		types.NamespacedName{
			Name:      v1alpha1.WorkspaceStorageName(workspace.Spec.UserName),
			Namespace: workspace.Namespace,
		}, workspaceStorage); err != nil {
		return nil, err
	}
	return workspaceStorage, nil
}
