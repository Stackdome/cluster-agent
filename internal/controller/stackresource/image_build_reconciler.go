package stackresource

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

// ImageBuildReconciler handles the creation of imagebuild resource for a stack resource.
type imageBuildReconciler struct {
	client.Client
	scheme *runtime.Scheme
}

// Reconcile processes StackResource objects that need image builds
func (r *imageBuildReconciler) reconcile(ctx context.Context, resource *v1alpha1.StackResource) (subReconcilerResult, error) {
	// Skip if no build spec is defined
	if resource.Spec.BuildSpec == nil {
		return resultNil, nil
	}

	// Check if ImageBuild already exists
	imageBuildName := generateImageBuildName(resource)
	existingImageBuild := &buildsv1alpha1.ImageBuild{}

	err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      imageBuildName,
			Namespace: resource.Namespace,
		},
		existingImageBuild,
	)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.createImageBuild(ctx, resource)
		}
		return resultNil, fmt.Errorf("failed to get ImageBuild: %w", err)
	}

	// Check if build is complete
	if isImageBuildComplete(existingImageBuild) {
		return resultNil, nil
	}

	// Build is still in progress
	reportStackResourceNotReady(resource, "ImageBuildInProgress", "Image build is still in progress")
	return resultStop, nil
}

// createImageBuild creates a new ImageBuild resource for a WorkspaceResource
func (r *imageBuildReconciler) createImageBuild(ctx context.Context, resource *v1alpha1.StackResource) (subReconcilerResult, error) {
	buildSpec := resource.Spec.BuildSpec
	imageBuild := &buildsv1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:        generateImageBuildName(resource),
			Namespace:   resource.Namespace,
			Labels:      createImageBuildLabels(resource),
			Annotations: createImageBuildAnnotations(resource),
		},
		Spec: buildsv1alpha1.ImageBuildSpec{
			ResourceName: resource.Name,
			SourceHash:   buildSpec.BuildSourceHash,
			ContextRef: buildsv1alpha1.ContextRef{
				VolumeName:     buildSpec.SourceVolumeName,
				DockerfilePath: buildSpec.DockerFilePath,
				Context:        buildSpec.BuildContext,
			},
			RegistryURL:      buildSpec.Registry.RepositoryURL,
			InsecureRegistry: buildSpec.Registry.Insecure,
		},
	}

	// Configure authentication if specified
	if err := r.configureRegistryAuth(resource, imageBuild); err != nil {
		return resultNil, err
	}

	// Set owner reference to the WorkspaceResource
	if err := controllerutil.SetOwnerReference(resource, imageBuild, r.scheme); err != nil {
		return resultNil, fmt.Errorf("failed to set controller reference: %w", err)
	}

	// Create the ImageBuild resource
	if err := r.Client.Create(ctx, imageBuild); err != nil {
		return resultNil, fmt.Errorf("failed to create ImageBuild: %w", err)
	}

	// Stop further reconciliation until the build completes
	return resultStop, nil
}

// configureRegistryAuth sets up authentication for the registry
func (r *imageBuildReconciler) configureRegistryAuth(resource *v1alpha1.StackResource, imageBuild *buildsv1alpha1.ImageBuild) error {
	buildSpec := resource.Spec.BuildSpec
	if buildSpec.Registry.Auth == nil {
		return nil
	}

	authURL, err := resource.RegistryAuthUrl()
	if err != nil {
		return fmt.Errorf("failed to get auth URL: %w", err)
	}

	// Configure auth based on registry type
	switch buildSpec.Registry.Auth.Type {
	case v1alpha1.RegistryAuthTypeDockerHub, v1alpha1.RegistryAuthTypeInClusterZotRegistry:
		if imageBuild.Spec.Auth == nil {
			imageBuild.Spec.Auth = &buildsv1alpha1.RegistryAuth{}
		}
		imageBuild.Spec.Auth.Type = buildSpec.Registry.Auth.Type
		imageBuild.Spec.Auth.DockerAuthSecretRef = &buildsv1alpha1.DockerAuthSecretRef{
			SecretName:      registrySecretName(authURL),
			SecretNamespace: resource.Namespace,
			AuthKey:         buildSpec.Registry.Auth.GetDockerConfigSecretKey(),
		}
	}

	return nil
}

// generateImageBuildName creates a unique name for the ImageBuild resource
func generateImageBuildName(resource *v1alpha1.StackResource) string {
	return buildsv1alpha1.ImageBuildName(resource.Name, resource.Spec.BuildSpec.BuildSourceHash)
}

// createImageBuildLabels creates labels for the ImageBuild resource
func createImageBuildLabels(resource *v1alpha1.StackResource) map[string]string {
	labels := make(map[string]string)
	if resource.Labels != nil {
		for k, v := range resource.Labels {
			labels[k] = v
		}
	}

	labels["stackdome.io/component"] = "image-build"
	labels["stackdome.io/part-of"] = resource.Name

	return labels
}

// createImageBuildAnnotations creates annotations for the ImageBuild resource
func createImageBuildAnnotations(resource *v1alpha1.StackResource) map[string]string {
	annotations := make(map[string]string)
	if resource.Annotations != nil {
		for k, v := range resource.Annotations {
			annotations[k] = v
		}
	}
	return annotations
}

// isImageBuildComplete checks if an ImageBuild is successfully completed
func isImageBuildComplete(build *buildsv1alpha1.ImageBuild) bool {
	return build.Status.Phase == buildsv1alpha1.BuildPhaseSuccess
}
