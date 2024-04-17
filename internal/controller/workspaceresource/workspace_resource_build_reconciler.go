package workspaceresource

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

const DefaultClusterLocalImageRegistry = "local-registry:5000"

type workspaceResourceBuildReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *workspaceResourceBuildReconciler) reconcile(ctx context.Context, resource *v1alpha1.WorkspaceResource) (subReconcilerResult, error) {
	if resource.Spec.ApplicationSourceSpec == nil {
		// This is a prebuilt image, we dont care if its uptodate.
		return resultNil, nil
	}
	logger := controller.LoggerFromContext(ctx)
	existingApplicationBuild := &v1alpha1.WorkspaceApplicationBuild{}
	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      ExpectedApplicationBuildName(resource),
			Namespace: resource.Namespace,
		},
		existingApplicationBuild,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return r.createApplicationBuild(ctx, resource)
		}
		return resultNil, err
	}
	// TODO: Cleanup failed, completed application builds.
	// Enhancement: Look at how we can rollback a workspaceresource to a previous build.
	availableCond := meta.FindStatusCondition(existingApplicationBuild.Status.Conditions, string(v1alpha1.WorkspaceApplicationBuildAvailable))
	if availableCond != nil && availableCond.Status == v1.ConditionTrue && availableCond.ObservedGeneration == existingApplicationBuild.Generation {
		logger.Info("successfully reconciled ws resource build")
		return resultNil, nil
	}
	// Build not yet complete/available
	reportWorkspaceResourceNotReady(resource, "ApplicationBuildNotYetReady", "Application build is not yet ready")
	return resultStop, nil
}

func (r *workspaceResourceBuildReconciler) createApplicationBuild(ctx context.Context, resource *v1alpha1.WorkspaceResource) (subReconcilerResult, error) {
	desiredApplicationBuild := &v1alpha1.WorkspaceApplicationBuild{
		ObjectMeta: v1.ObjectMeta{
			Name:      ExpectedApplicationBuildName(resource),
			Namespace: resource.Namespace,
		},
		Spec: v1alpha1.WorkspaceApplicationBuildSpec{
			SourceHash: resource.Spec.RunSourceHash,
			ContextRef: v1alpha1.ContextRef{
				WorkspaceStorageName: resource.Spec.WorkspaceStorageRef.WorkspaceStorageName,
				ResourceName:         resource.Spec.WorkspaceStorageRef.ResourceName,
				DockerfilePath:       resource.Spec.ApplicationSourceSpec.DockerFile,
				Context:              resource.Spec.ApplicationSourceSpec.Context,
			},
			Registry: GetImageRegistry(resource),
		},
	}
	if err := controllerutil.SetControllerReference(resource, desiredApplicationBuild, r.Scheme); err != nil {
		return resultNil, err
	}
	// We dont want other subreconcilers to run before the application build is complete.
	return resultStop, r.Client.Create(ctx, desiredApplicationBuild)
}

func GetImageRegistry(resource *v1alpha1.WorkspaceResource) string {
	if resource.Spec.ImageRegistry != nil {
		return *resource.Spec.ImageRegistry
	}
	return DefaultClusterLocalImageRegistry
}

func ExpectedApplicationBuildName(resource *v1alpha1.WorkspaceResource) string {
	return fmt.Sprintf("%s-%s", resource.Name, resource.Spec.RunSourceHash)
}
