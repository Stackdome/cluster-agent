package stackresource

import (
	"bytes"
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
		if stackResource.Spec.BuildSpec.Repository.Auth == nil {
			return resultNil, nil
		}
		return r.reconcileCredentials(ctx, stackResource, stackResource.Spec.BuildSpec.Repository)
	default:
		if stackResource.Spec.ImageSpec.PullAuth == nil {
			return resultNil, nil
		}
		return r.reconcileDockerConfigAuth(ctx, stackResource.Spec.ImageSpec.PullAuth.DockerConfigAuth, stackResource)
	}
}

// reconcileDockerConfigAuth ensures a referenced docker-config secret exists
// and is owned by the StackResource.
func (r *registryAuthReconciler) reconcileDockerConfigAuth(
	ctx context.Context,
	dockerConfigAuth *corev1alpha1.DockerConfigAuth,
	stackResource *corev1alpha1.StackResource,
) (subReconcilerResult, error) {
	secret := &corev1.Secret{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Name:      dockerConfigAuth.SecretRef.Name,
		Namespace: stackResource.Namespace,
	}, secret); err != nil {
		if errors.IsNotFound(err) {
			reportStackResourceNotReady(stackResource, "DockerConfigJsonSecretNotFound", "Docker credentials secret not found")
			return resultStop, nil
		}
		return resultNil, fmt.Errorf("failed to get docker config json secret: %w", err)
	}

	if err := controllerutil.SetControllerReference(stackResource, secret, r.scheme); err != nil {
		return resultNil, err
	}

	ok, err := controllerutil.HasOwnerReference(secret.OwnerReferences, stackResource, r.scheme)
	if err != nil {
		return resultNil, fmt.Errorf("failed to check owner reference: %w", err)
	}
	if !ok {
		return resultNil, r.client.Update(ctx, secret)
	}

	return resultNil, nil
}

// reconcileCredentials handles the new RegistryCredentialsSpec auth model.
// It dispatches to the appropriate handler based on whether DockerConfig or
// Basic credentials are provided.
func (r *registryAuthReconciler) reconcileCredentials(
	ctx context.Context, sr *corev1alpha1.StackResource, repoSpec corev1alpha1.ImageRepositorySpec) (subReconcilerResult, error) {
	switch {
	case repoSpec.Auth.DockerConfig != nil:
		return r.reconcileDockerConfigAuth(ctx, repoSpec.Auth.DockerConfig, sr)
	case repoSpec.Auth.Basic != nil:
		resolved, err := registry.ResolveImageRepository(ctx, r.client, sr.Namespace, repoSpec, sr.Spec.BuildSpec.SourceRevision.GetSourceRevisionString())
		if err != nil {
			reportStackResourceNotReady(sr, "RepositoryResolveFailed", fmt.Sprintf("failed to resolve image repository: %v", err))
			return resultStop, nil
		}
		return r.synthesizeFromBasic(ctx, sr, repoSpec.Auth.Basic, resolved.AuthURL)
	default:
		return resultNil, nil
	}
}

// synthesizeFromBasic reads a basic-auth Secret (username + password keys),
// builds a Docker config JSON from those credentials, and creates or updates
// a dockerconfigjson Secret owned by the StackResource.
func (r *registryAuthReconciler) synthesizeFromBasic(ctx context.Context, sr *corev1alpha1.StackResource, basic *corev1alpha1.BasicAuthCredentials, authURL string) (subReconcilerResult, error) {
	// SecretRef.Namespace is ignored — the source secret must live in the same
	// namespace as the StackResource. Cross-namespace reads would require
	// additional RBAC and break the ownership model.
	src := &corev1.Secret{}
	if err := r.client.Get(ctx, client.ObjectKey{Name: basic.SecretRef.Name, Namespace: sr.Namespace}, src); err != nil {
		if errors.IsNotFound(err) {
			reportStackResourceNotReady(sr, "RegistryCredentialsMissing", "image repository credentials secret not found")
			return resultStop, nil
		}
		return resultNil, err
	}
	user := string(src.Data[basic.UsernameKey])
	pass := string(src.Data[basic.PasswordKey])
	cfg := registry.NewDockerConfigJSON([]registry.AuthCreds{{Username: user, Password: pass, AuthUrl: authURL}})
	raw, err := cfg.AsJSON()
	if err != nil {
		return resultNil, err
	}

	secretName := sr.SynthesizedDockerConfigSecretName()
	out := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: sr.Namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: raw},
	}
	if err := controllerutil.SetControllerReference(sr, out, r.scheme); err != nil {
		return resultNil, err
	}

	existing := &corev1.Secret{}
	err = r.client.Get(ctx, client.ObjectKey{Name: secretName, Namespace: sr.Namespace}, existing)
	if err != nil {
		if errors.IsNotFound(err) {
			if err := r.client.Create(ctx, out); err != nil {
				return resultNil, err
			}
			return resultNil, nil
		}
		return resultNil, err
	}
	if bytes.Equal(existing.Data[corev1.DockerConfigJsonKey], out.Data[corev1.DockerConfigJsonKey]) {
		return resultNil, nil
	}
	existing.Data = out.Data
	return resultNil, r.client.Update(ctx, existing)
}
