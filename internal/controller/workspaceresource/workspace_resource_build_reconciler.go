package workspaceresource

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

const DefaultClusterLocalImageRegistry = "local-registry:5000"

type workspaceResourceBuildReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *workspaceResourceBuildReconciler) reconcile(ctx context.Context, resource *v1alpha1.WorkspaceResource) (subReconcilerResult, error) {
	if resource.Spec.ApplicationBuildSpec == nil {
		// This is a prebuilt image, nothing to build.
		return resultNil, nil
	}

	existingApplicationBuild := &buildsv1alpha1.WorkspaceApplicationBuild{}
	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      ApplicationBuildName(resource),
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
	if applicationBuildComplete(existingApplicationBuild) {
		return resultNil, nil
	}
	// Build not yet complete/available
	reportWorkspaceResourceNotReady(resource, "ApplicationBuildNotYetReady", "Application build is not yet ready")
	return resultStop, nil
}

func (r *workspaceResourceBuildReconciler) createApplicationBuild(ctx context.Context, resource *v1alpha1.WorkspaceResource) (subReconcilerResult, error) {
	desiredApplicationBuild := &buildsv1alpha1.WorkspaceApplicationBuild{
		ObjectMeta: v1.ObjectMeta{
			Name:        ApplicationBuildName(resource),
			Namespace:   resource.Namespace,
			Labels:      resource.Labels,
			Annotations: resource.Annotations,
		},
		Spec: buildsv1alpha1.WorkspaceApplicationBuildSpec{
			ResourceName: resource.Name,
			SourceHash:   resource.Spec.ApplicationBuildSpec.BuildSourceHash,
			ContextRef: buildsv1alpha1.ContextRef{
				VolumeName:     resource.Spec.ApplicationBuildSpec.VolumeName,
				DockerfilePath: resource.Spec.ApplicationBuildSpec.DockerFile,
				Context:        resource.Spec.ApplicationBuildSpec.Context,
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

func ApplicationBuildName(resource *v1alpha1.WorkspaceResource) string {
	return fmt.Sprintf("%s-%s", resource.Name, resource.Spec.ApplicationBuildSpec.BuildSourceHash[:7])
}
