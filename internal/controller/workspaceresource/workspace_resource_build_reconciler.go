package workspaceresource

import (
	"context"
	"fmt"
	"path/filepath"

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
	if resource.Spec.ApplicationBuildSpec == nil {
		// This is a prebuilt image, we dont care if its uptodate.
		return resultNil, nil
	}
	logger := controller.LoggerFromContext(ctx)
	existingApplicationBuild := &v1alpha1.WorkspaceApplicationBuild{}
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
	volumeInfo, err := r.getVolumeInfo(ctx, resource)
	if err != nil {
		return resultNil, err
	}

	volumeMountForIntialization := []v1alpha1.VolumeMountForInitialization{}
	for _, mount := range resource.Spec.VolumeMounts {
		source := mount.Source
		destination := mount.Destination
		//Ex: deps/node_modules:/app/nodemodules. Here, deps is the name of the volume and /node_modules is the subpath.
		sourceVolumeName := filepath.SplitList(source)[0]
		sourceSubPath := filepath.Join(filepath.SplitList(source)[1:]...)
		referencedVolume := volumeInfo[sourceVolumeName]
		// Only initialize volumes of type v1alpha1.EmptyStorageType.
		if referencedVolume.Spec.Type == v1alpha1.EmptyStorageType {
			volumeMountForIntialization = append(volumeMountForIntialization, v1alpha1.VolumeMountForInitialization{
				ContainerMountPath: destination,
				PvcName:            referencedVolume.Status.PvcName,
				SubPath:            sourceSubPath,
			})
		}
	}

	desiredApplicationBuild := &v1alpha1.WorkspaceApplicationBuild{
		ObjectMeta: v1.ObjectMeta{
			Name:      ApplicationBuildName(resource),
			Namespace: resource.Namespace,
		},
		Spec: v1alpha1.WorkspaceApplicationBuildSpec{
			ResourceName: resource.Name,
			SourceHash:   resource.Spec.ApplicationBuildSpec.BuildSourceHash,
			ContextRef: v1alpha1.ContextRef{
				VolumeName:     resource.Spec.ApplicationBuildSpec.VolumeName,
				DockerfilePath: resource.Spec.ApplicationBuildSpec.DockerFile,
				Context:        resource.Spec.ApplicationBuildSpec.Context,
			},
			Registry:     GetImageRegistry(resource),
			VolumeMounts: volumeMountForIntialization,
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
	return fmt.Sprintf("%s-%s", resource.Name, resource.Spec.ApplicationBuildSpec.BuildSourceHash)
}

func (r *workspaceResourceBuildReconciler) getVolumeInfo(ctx context.Context, resource *v1alpha1.WorkspaceResource) (map[string]*v1alpha1.WorkspaceVolume, error) {
	res := make(map[string]*v1alpha1.WorkspaceVolume)
	for _, mount := range resource.Spec.VolumeMounts {
		sourceVolumeName := filepath.SplitList(mount.Source)[0]
		referencedVolume := &v1alpha1.WorkspaceVolume{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: sourceVolumeName, Namespace: resource.Namespace}, referencedVolume); err != nil {
			return nil, fmt.Errorf("failed to get the referenced volume '%s' in resource '%s': %w", sourceVolumeName, resource.Name, err)
		}
		res[sourceVolumeName] = referencedVolume
	}
	return res, nil
}
