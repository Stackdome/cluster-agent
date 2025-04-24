package stackresource

import (
	"context"
	"crypto/sha256"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/pkg/registry"
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
	dockerCredentialsSecret := &corev1.Secret{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Name:      dockerConfigAuth.CredentialsRef.SecretName,
		Namespace: dockerConfigAuth.CredentialsRef.SecretNamespace,
	}, dockerCredentialsSecret); err != nil {
		logger.Error(err, "Failed to get docker credentials secret")
		if errors.IsNotFound(err) {
			// Update the WorkspaceResource status if the credentials secret doesn't exist
			reportStackResourceNotReady(stackResource, "DockerCredentialsSecretNotFound", "Docker credentials secret not found")
			return resultStop, nil
		}
		return resultNil, fmt.Errorf("failed to get docker credentials secret: %w", err)
	}

	// Extract username from the credentials secret
	username, ok := dockerCredentialsSecret.Data[dockerConfigAuth.CredentialsRef.UsernameKey]
	if !ok {
		logger.Info("Docker credentials secret username key not found")
		reportStackResourceNotReady(stackResource, "DockerCredentialsSecretUsernameKeyNotFound", "Docker credentials secret username key not found")
		return resultStop, nil
	}

	// Extract password from the credentials secret
	password, ok := dockerCredentialsSecret.Data[dockerConfigAuth.CredentialsRef.PasswordKey]
	if !ok {
		logger.Info("Docker credentials secret password key not found")
		reportStackResourceNotReady(stackResource, "DockerCredentialsSecretPasswordKeyNotFound", "Docker credentials secret password key not found")
		return resultStop, nil
	}

	// Get the authentication URL for the registry
	authUrl, err := stackResource.RegistryAuthUrl()
	if err != nil {
		return resultNil, fmt.Errorf("failed to get auth URL: %w", err)
	}

	// Create authentication credentials for the Docker config
	authCreds := registry.AuthCreds{
		Username: string(username),
		Password: string(password),
		AuthUrl:  authUrl,
	}

	// Create a new Docker config JSON with the authentication credentials
	dockerConfig := registry.NewDockerConfigJSON([]registry.AuthCreds{authCreds})

	// Convert the Docker config to JSON format
	dockerConfigJson, err := dockerConfig.AsJSON()
	if err != nil {
		return resultNil, fmt.Errorf("failed to convert docker config to JSON: %w", err)
	}

	// Get the key name to use in the Kubernetes secret
	dockerConfigKeyInSecret := registryAuth.GetDockerConfigSecretKey()

	// Create a Secret object with the Docker config JSON
	dockerConfigJsonSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registrySecretName(authUrl),
			Namespace: stackResource.Namespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			dockerConfigKeyInSecret: dockerConfigJson,
		},
	}

	// Set the WorkspaceResource as the owner of the secret
	// This ensures the secret is deleted when the WorkspaceResource is deleted
	if err := controllerutil.SetControllerReference(stackResource, dockerConfigJsonSecret, r.scheme); err != nil {
		return resultNil, err
	}

	// Check if the secret already exists
	existingSecret := &corev1.Secret{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Name:      dockerConfigJsonSecret.Name,
		Namespace: dockerConfigJsonSecret.Namespace,
	}, existingSecret); err != nil {
		if errors.IsNotFound(err) {
			// Create a new secret if it doesn't exist
			logger.Info("Docker config secret not found, creating a new one")
			return resultNil, r.client.Create(ctx, dockerConfigJsonSecret)
		}
		return resultNil, fmt.Errorf("failed to get existing registry secret: %w", err)
	}

	// Update the existing secret if the Docker config has changed
	if existingSecret.Data[dockerConfigKeyInSecret] == nil || string(existingSecret.Data[dockerConfigKeyInSecret]) != string(dockerConfigJson) {
		existingSecret.Data[dockerConfigKeyInSecret] = dockerConfigJson
		return resultNil, r.client.Update(ctx, existingSecret)
	}

	// No changes needed, return success
	return resultNil, nil
}

// registrySecretName generates a deterministic name for registry secrets based on the registry URL
// This ensures the same registry always gets the same secret name, which helps with idempotency
func registrySecretName(registryAuthUrl string) string {
	// Create a deterministic name based on the registry URL by using a hash
	// Only using first 8 characters of the hash to keep the name reasonably short
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(registryAuthUrl)))[0:8]
	return fmt.Sprintf("registry-auth-%s", hash)
}
