package workspace

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

func (r *WorkspaceReconciler) ReconcileWorkspaceResources(ctx context.Context, workspace *v1alpha1.Workspace) (subReconcilerResult, error) {
	desiredWorkspaceResources := make([]*v1alpha1.WorkspaceResource, 0)
	for _, workspaceResourceSpec := range workspace.Spec.Resources {
		desiredWorkspaceResource := constructWorkspaceResourceCR(workspace, &workspaceResourceSpec)
		desiredWorkspaceResources = append(desiredWorkspaceResources, desiredWorkspaceResource)
	}

	existingWRs := make([]*v1alpha1.WorkspaceResource, 0)
	for _, desiredWorkspaceResource := range desiredWorkspaceResources {
		existingWR, err := r.reconcileWorkspaceResource(ctx, workspace, desiredWorkspaceResource)
		if err != nil {
			return resultNil, err
		}
		existingWRs = append(existingWRs, existingWR)
	}

	for _, wr := range existingWRs {
		if !r.workspaceResourceAvailable(wr) {
			reportWorkspaceNotReady(workspace, "WorkspaceResourcesNotReady", fmt.Sprintf("WorkspaceResource: '%s' not ready", wr.Name))
			return resultNil, nil
		}
	}
	reportWorkspaceReady(workspace)
	return resultNil, nil
}

func (r *WorkspaceReconciler) workspaceResourceAvailable(wr *v1alpha1.WorkspaceResource) bool {
	availableCond := meta.FindStatusCondition(wr.Status.Conditions, string(v1alpha1.WorkspaceResourceStatusAvailable))
	if availableCond != nil && availableCond.Status == v1.ConditionTrue && availableCond.ObservedGeneration == wr.Generation {
		return true
	}
	return false
}

func (r *WorkspaceReconciler) reconcileWorkspaceResource(
	ctx context.Context,
	workspace *v1alpha1.Workspace,
	desiredWR *v1alpha1.WorkspaceResource) (*v1alpha1.WorkspaceResource, error) {
	if err := controllerutil.SetControllerReference(workspace, desiredWR, r.Scheme); err != nil {
		return nil, err
	}

	existingWR := &v1alpha1.WorkspaceResource{}
	if err := r.Client.Get(ctx, controller.GetNamespacedName(desiredWR), existingWR); err != nil {
		if apierrors.IsNotFound(err) {
			return desiredWR, r.Client.Create(ctx, desiredWR)
		}
		return nil, err
	}

	if !equality.Semantic.DeepDerivative(desiredWR.Spec, existingWR.Spec) {
		existingWR.Spec = desiredWR.Spec
		return existingWR, r.Client.Update(ctx, existingWR)
	}
	return existingWR, nil
}

func constructWorkspaceResourceCR(workspace *v1alpha1.Workspace, resourceSpec *v1alpha1.ResourceSpec) *v1alpha1.WorkspaceResource {
	return &v1alpha1.WorkspaceResource{
		ObjectMeta: v1.ObjectMeta{
			Name:        v1alpha1.WorkspaceResourceName(resourceSpec.Name),
			Namespace:   workspace.Namespace,
			Labels:      workspace.Labels,
			Annotations: workspace.Annotations,
		},
		Spec: v1alpha1.WorkspaceResourceSpec{
			ImageRegistry:           resourceSpec.Spec.ImageRegistry,
			ApplicationBuildSpec:    resourceSpec.Spec.ApplicationBuildSpec,
			PrebuiltApplicationSpec: resourceSpec.Spec.PrebuiltApplicationSpec,
			EnvironmentVariables:    resourceSpec.Spec.EnvironmentVariables,
			VolumeMounts:            resourceSpec.Spec.VolumeMounts,
			Ports:                   resourceSpec.Spec.Ports,
			Command:                 resourceSpec.Spec.Command,
			Init:                    resourceSpec.Spec.Init,
			Args:                    resourceSpec.Spec.Args,
			DependsOn:               resourceSpec.Spec.DependsOn,
			RestartRequest:          resourceSpec.Spec.RestartRequest,
			StateFul:                resourceSpec.Spec.StateFul,
		},
	}
}
