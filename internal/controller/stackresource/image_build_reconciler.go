package stackresource

import (
	"context"
	"fmt"
	"sort"

	"github.com/samber/lo"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

// ImageBuildReconciler handles the creation of imagebuild resource for a stack resource.
type imageBuildReconciler struct {
	client.Client
	scheme       *runtime.Scheme
	historyLimit int
}

// Reconcile processes StackResource objects that need image builds
func (r *imageBuildReconciler) reconcile(ctx context.Context, resource *v1alpha1.StackResource) (subReconcilerResult, error) {
	// Skip if no build spec is defined
	if resource.Spec.BuildSpec == nil {
		return resultNil, nil
	}

	builds, err := r.listImageBuilds(ctx, resource)
	if err != nil {
		return resultNil, err
	}

	if err := r.cancelStaleImageBuilds(ctx, resource, builds); err != nil {
		return resultNil, err
	}

	if err := r.enforceImageBuildRetention(ctx, resource, builds); err != nil {
		return resultNil, err
	}

	imageBuildName := generateImageBuildName(resource)
	existingImageBuild := &buildsv1alpha1.ImageBuild{}

	err = r.Client.Get(ctx,
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

	// Source revision was re-used after a previous build was cancelled.
	// Cancelled is immutable, so delete the old record and create a fresh build.
	if existingImageBuild.Spec.Cancelled {
		if err := r.Client.Delete(ctx, existingImageBuild, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
			return resultNil, fmt.Errorf("failed to delete cancelled ImageBuild %s: %w", existingImageBuild.Name, err)
		}
		return r.createImageBuild(ctx, resource)
	}

	if imageBuildFailed(existingImageBuild) {
		setResourceCondition(resource, v1alpha1.StackResourceBuildReady, false, "BuildFailed", "application build failed terminally")
		reportStackResourceFailed(resource, "BuildFailed",
			fmt.Sprintf("ImageBuild %s reached terminal Failed phase", existingImageBuild.Name))
		return resultStop, nil
	}

	if isImageBuildComplete(existingImageBuild) {
		return resultNil, nil
	}

	reportStackResourceNotReady(resource, "ImageBuildInProgress", "Image build is still in progress")
	return resultStop, nil
}

func (r *imageBuildReconciler) listImageBuilds(ctx context.Context, resource *v1alpha1.StackResource) ([]buildsv1alpha1.ImageBuild, error) {
	var buildList buildsv1alpha1.ImageBuildList
	if err := r.Client.List(ctx, &buildList,
		client.InNamespace(resource.Namespace),
		client.MatchingLabels{
			"stackdome.io/part-of":   resource.Name,
			"stackdome.io/component": "image-build",
		},
	); err != nil {
		return nil, fmt.Errorf("failed to list ImageBuilds: %w", err)
	}
	return buildList.Items, nil
}

// cancelStaleImageBuilds cancels any ImageBuilds that are not the current build.
func (r *imageBuildReconciler) cancelStaleImageBuilds(ctx context.Context, resource *v1alpha1.StackResource, builds []buildsv1alpha1.ImageBuild) error {
	logger := controller.LoggerFromContext(ctx)
	currentBuildName := generateImageBuildName(resource)

	buildsToCancel := lo.Filter(builds, func(build buildsv1alpha1.ImageBuild, _ int) bool {
		return isOwnedByStackResource(build, resource) &&
			!build.Spec.Cancelled &&
			!isImageBuildTerminal(&build) &&
			build.Name != currentBuildName
	})

	errGroup, ctx := errgroup.WithContext(ctx)
	for _, build := range buildsToCancel {
		errGroup.Go(func() error {
			build.Spec.Cancelled = true
			if err := r.Client.Update(ctx, &build); err != nil {
				return fmt.Errorf("failed to cancel stale ImageBuild %s: %w", build.Name, err)
			}
			logger.Info("cancelled stale ImageBuild", "imageBuild", build.Name)
			return nil
		})
	}

	return errGroup.Wait()
}

// enforceImageBuildRetention deletes terminal builds beyond the history limit.
func (r *imageBuildReconciler) enforceImageBuildRetention(ctx context.Context, resource *v1alpha1.StackResource, builds []buildsv1alpha1.ImageBuild) error {
	logger := controller.LoggerFromContext(ctx)
	currentBuildName := generateImageBuildName(resource)

	sort.Slice(builds, func(i, j int) bool {
		return builds[j].CreationTimestamp.Before(&builds[i].CreationTimestamp)
	})

	var errGroup errgroup.Group
	if r.historyLimit > 0 && len(builds) > r.historyLimit {
		for _, build := range builds[r.historyLimit:] {
			if isImageBuildTerminal(&build) && isOwnedByStackResource(build, resource) && build.Name != currentBuildName {
				errGroup.Go(func() error {
					if err := r.Client.Delete(ctx, &build, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
						return fmt.Errorf("failed to delete old ImageBuild %s: %w", build.Name, err)
					}
					logger.Info("deleted ImageBuild beyond retention limit", "imageBuild", build.Name)
					return nil
				})
			}
		}
	}

	return errGroup.Wait()
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
			ResourceName:   resource.Name,
			SourceRevision: buildSpec.SourceRevision,
			BuildContext: buildsv1alpha1.BuildContextSpec{
				DockerfilePath: buildSpec.DockerFilePath,
				ContextPath:    buildSpec.BuildContext,
				ContextSource:  buildSpec.SourceContext.DeepCopy(),
			},
			Repository: *buildSpec.Repository.DeepCopy(),
			BuildArgs:  buildSpec.BuildArgs,
		},
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

// generateImageBuildName creates a unique name for the ImageBuild resource
func generateImageBuildName(resource *v1alpha1.StackResource) string {
	return buildsv1alpha1.ImageBuildName(resource.Name, resource.Spec.BuildSpec.SourceRevision.GetSourceRevisionString())
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

// isImageBuildTerminal returns true when the build has reached a terminal state.
// Terminal conditions: BuildAvailable=True (success), BuildFailed=True, or BuildCancelled=True.
func isImageBuildTerminal(build *buildsv1alpha1.ImageBuild) bool {
	for _, cond := range build.Status.Conditions {
		switch buildsv1alpha1.BuildStatusCondition(cond.Type) {
		case buildsv1alpha1.BuildAvailable, buildsv1alpha1.BuildFailed, buildsv1alpha1.BuildCancelled:
			if cond.Status == metav1.ConditionTrue {
				return true
			}
		}
	}
	return false
}

func isOwnedByStackResource(build buildsv1alpha1.ImageBuild, resource *v1alpha1.StackResource) bool {
	for _, ref := range build.OwnerReferences {
		if ref.UID == resource.UID {
			return true
		}
	}
	return false
}
