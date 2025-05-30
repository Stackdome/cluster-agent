package stackresource

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

// registryAuthReconciler manages the registry authentication for StackResource object
// This reconciler creates and maintains Docker config secrets needed for pulling or pushing images
type registryAuthReconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

// reconcile is the main reconciliation function for registry authentication
// It determines which type of reconciliation to perform based on the WorkspaceResource spec
func (r *registryAuthReconciler) reconcile(ctx context.Context, stackResource *corev1alpha1.StackResource) (subReconcilerResult, error) {
	switch {
	case stackResource.Spec.BuildSpec != nil:
		// If BuildSpec exists, use registry authentication from the build specification
		// This is used when the WorkspaceResource needs to push built images
		registrySpec := stackResource.Spec.BuildSpec.Registry
		return r.reconcileRegistryAuth(ctx, registrySpec.Auth, stackResource, registrySpec.RepositoryURL)
	default:
		// Otherwise, use registry authentication from the image specification
		// This is used when the WorkspaceResource needs to pull existing images
		return r.reconcileRegistryAuth(ctx, stackResource.Spec.ImageSpec.PullAuth, stackResource, stackResource.Spec.ImageSpec.Image)
	}
}

// reconcileRegistryAuth handles authentication configuration for a specific registry
// It determines the appropriate authentication method based on the registry type
func (r *registryAuthReconciler) reconcileRegistryAuth(
	ctx context.Context,
	registryAuth *corev1alpha1.RegistryAuth,
	stackResource *corev1alpha1.StackResource,
	registryUrl string,
) (subReconcilerResult, error) {
	if registryAuth == nil {
		// If no registry authentication is provided, return success
		return resultNil, nil
	}
	switch registryAuth.Type {
	case corev1alpha1.RegistryAuthTypeDockerHub, corev1alpha1.RegistryAuthTypeInClusterZotRegistry:
		// For DockerHub and in-cluster Zot registry, use docker config authentication
		return r.reconcileDockerConfigAuthSecret(ctx, registryAuth, stackResource, registryUrl)
	default:
		// Return an error for unsupported registry types
		return resultNil, fmt.Errorf("unsupported registry auth type: %s", registryAuth.Type)
	}
}

// reconcileDockerConfigAuthSecret creates or updates a Kubernetes secret containing
// Docker registry credentials in the format required for image pulling/pushing
func (r *registryAuthReconciler) reconcileDockerConfigAuthSecret(
	ctx context.Context,
	registryAuth *corev1alpha1.RegistryAuth,
	stackResource *corev1alpha1.StackResource,
	registryUrl string,
) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)

	// Validate that Docker config authentication details are provided
	if registryAuth.DockerConfigAuth == nil {
		logger.Info("Docker config auth is nil")
		reportStackResourceNotReady(stackResource, "DockerConfigAuthDetailsMissing", "Docker config auth is missing")
		return resultStop, nil
	}

	dockerConfigAuth := registryAuth.DockerConfigAuth

	// Fetch the secret containing registry credentials (username/password)
	dockerConfigJsonSecret := &corev1.Secret{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Name:      dockerConfigAuth.SecretRef.Name,
		Namespace: dockerConfigAuth.SecretRef.Namespace,
	}, dockerConfigJsonSecret); err != nil {
		if errors.IsNotFound(err) {
			reportStackResourceNotReady(stackResource, "DockerConfigJsonSecretNotFound", "Docker credentials secret not found")
			return resultStop, nil
		}
		return resultNil, fmt.Errorf("failed to get docker config json secret: %w", err)
	}

	// set owner refs.
	if err := controllerutil.SetControllerReference(stackResource, dockerConfigJsonSecret, r.scheme); err != nil {
		return resultNil, err
	}

	ok, err := controllerutil.HasOwnerReference(dockerConfigJsonSecret.OwnerReferences, stackResource, r.scheme)
	if err != nil {
		return resultNil, fmt.Errorf("failed to check owner reference: %w", err)
	}
	if !ok {
		return resultNil, r.client.Update(ctx, dockerConfigJsonSecret)
	}

	return resultNil, nil
}
