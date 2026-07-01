package workload

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

func (r *Reconciler) getImageBuild(ctx context.Context, resource *v1alpha1.StackResource) (*buildsv1alpha1.ImageBuild, error) {
	existingApplicationBuild := &buildsv1alpha1.ImageBuild{}
	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      buildsv1alpha1.ImageBuildName(resource.Name, resource.Spec.BuildSpec.SourceRevision.GetSourceRevisionString()),
			Namespace: resource.Namespace,
		},
		existingApplicationBuild,
	); err != nil {
		return nil, err
	}
	return existingApplicationBuild, nil
}

func (r *Reconciler) getImageForResource(ctx context.Context, resource *v1alpha1.StackResource) (*string, error) {
	if resource.Spec.BuildSpec != nil {
		requiredBuild, err := r.getImageBuild(ctx, resource)
		if err != nil {
			return nil, err
		}
		return ptr.To(requiredBuild.Status.ImageUrl), nil
	}
	return ptr.To(resource.Spec.ImageSpec.Image), nil
}

func (r *Reconciler) resolveAndSetImagePullSecret(ctx context.Context, resource *v1alpha1.StackResource, podSpec *corev1.PodSpec) error {
	if !resource.NeedsPullSecret() {
		return nil
	}
	secretName, err := r.resolveImagePullSecretName(ctx, resource)
	if err != nil {
		return err
	}
	podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: secretName}}
	return nil
}

func (r *Reconciler) resolveImagePullSecretName(ctx context.Context, resource *v1alpha1.StackResource) (string, error) {
	var secretRef string

	if resource.HasBuildSpec() {
		if resource.Spec.BuildSpec.Repository.Auth == nil {
			return "", fmt.Errorf("build spec has no repository auth")
		}
		auth := resource.Spec.BuildSpec.Repository.Auth
		switch {
		case auth.DockerConfig != nil:
			secretRef = auth.DockerConfig.SecretRef.Name
		case auth.Basic != nil:
			secretRef = resource.SynthesizedDockerConfigSecretName()
		default:
			return "", fmt.Errorf("build spec has no valid auth credentials")
		}
	} else {
		if resource.Spec.ImageSpec.PullAuth == nil || resource.Spec.ImageSpec.PullAuth.DockerConfigAuth == nil {
			return "", fmt.Errorf("image spec has no pull auth")
		}
		secretRef = resource.Spec.ImageSpec.PullAuth.DockerConfigAuth.SecretRef.Name
	}

	secret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: secretRef, Namespace: resource.Namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("docker config secret not found: %w", err)
		}
		return "", fmt.Errorf("failed to get docker config secret: %w", err)
	}
	return secret.Name, nil
}

func (r *Reconciler) requiresRestart(resource *v1alpha1.StackResource) bool {
	lastRestartProcessedAt := resource.Status.LastRestartRequestProcessedAt
	currentRestartRequest := resource.Spec.RestartRequest
	switch {
	case currentRestartRequest != nil && lastRestartProcessedAt == nil:
		return true
	case currentRestartRequest != nil && currentRestartRequest.UTC().After(lastRestartProcessedAt.Time.UTC()):
		return true
	default:
		return false
	}
}

func imageBuildComplete(imageBuild *buildsv1alpha1.ImageBuild) bool {
	availableCond := meta.FindStatusCondition(imageBuild.Status.Conditions, string(buildsv1alpha1.BuildAvailable))
	if availableCond != nil && availableCond.Status == metav1.ConditionTrue {
		return true
	}
	return false
}
