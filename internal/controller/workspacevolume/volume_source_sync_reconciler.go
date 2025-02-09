package workspacevolume

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/pkg/volumesync"
)

const (
	ResourceNameLabel = "volumesync.stackdome.io/resourceName"
)

func (r *WorkspaceVolumeReconciler) reconcileVolumeSource(ctx context.Context, volume *v1alpha1.WorkspaceVolume) (subReconcilerResult, error) {
	if shouldSkipVolumeSourceReconcile(volume) {
		return resultNil, nil
	}

	subReconcilers := []func(context.Context, *v1alpha1.WorkspaceVolume) (subReconcilerResult, error){
		r.reconcileLocalVolumeSource,
		r.reconcileBuildArtifactsSources,
	}

	for _, subReconciler := range subReconcilers {
		res, err := subReconciler(ctx, volume)
		if err != nil {
			return resultNil, err
		}
		// If the sub-reconciler has a result, stop further reconcilers and return it.
		if res != resultNil {
			return res, nil
		}
	}
	return resultNil, nil
}

// NOOP, Syncing local volume source is done from the client side.
func (r *WorkspaceVolumeReconciler) reconcileLocalVolumeSource(ctx context.Context, volume *v1alpha1.WorkspaceVolume) (subReconcilerResult, error) {
	return resultNil, nil
}

func (r *WorkspaceVolumeReconciler) reconcileBuildArtifactsSources(ctx context.Context, volume *v1alpha1.WorkspaceVolume) (subReconcilerResult, error) {
	if volume.Spec.Source.BuildArtifacts == nil {
		return resultNil, nil
	}

	resourceBuildArtififactSrcsMap := buildArtifactSrcsGroupedByResource(volume)

	resourceBuildMap, err := r.getApplicationBuildsForResources(ctx, volume)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// We will get requueued when the application build/workspace resource is created.
			return resultStop, nil
		}
		return resultNil, err
	}

	for resourceRef, buildsrcs := range resourceBuildArtififactSrcsMap {
		applicationBuild := resourceBuildMap[resourceRef]
		if applicationBuild == nil {
			return resultNil, fmt.Errorf("application build not found for resource '%s'", resourceRef)
		}
		if err := r.reconcileBuildArtifactsSrcsForResource(ctx, volume, resourceRef, applicationBuild, buildsrcs); err != nil {
			return resultNil, err
		}
	}
	return resultNil, nil
}

func (r *WorkspaceVolumeReconciler) reconcileBuildArtifactsSrcsForResource(
	ctx context.Context,
	volume *v1alpha1.WorkspaceVolume,
	resourceName string,
	applicationBuild *buildsv1alpha1.WorkspaceApplicationBuild,
	buildArtifacts []*v1alpha1.BuildArtifactSource) error {
	if applicationBuildAvailable(applicationBuild) {
		desiredJob := volumesync.CreateBuildArtifactsVolumeSyncJob(volume, buildArtifacts, applicationBuild)
		desiredJob.Labels = map[string]string{
			ResourceNameLabel: resourceName,
		}
		if err := ctrl.SetControllerReference(volume, desiredJob, r.Scheme); err != nil {
			return err
		}

		existingJob := &batchv1.Job{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(desiredJob), existingJob); err != nil {
			if apierrors.IsNotFound(err) {
				return r.Client.Create(ctx, desiredJob)
			}
			return fmt.Errorf("failed to get existing build artifacts sync job: %w", err)
		}

		jobcompletedCond := findJobCompleteCondition(existingJob)
		if jobcompletedCond != nil && jobcompletedCond.Status == corev1.ConditionTrue {
			volume.Status.SetBuildArtifactSyncStatus(
				v1alpha1.ResourceRef(resourceName),
				applicationBuild.ShortBuildSrcHashFromStatus(),
				v1alpha1.BuildArtifactSyncStatusCompleted,
			)
			// TODO: Cleanup failed and completed jobs.
			return nil
		}
	}

	failedCond := meta.FindStatusCondition(applicationBuild.Status.Conditions, string(buildsv1alpha1.WorkspaceApplicationBuildFailed))
	if failedCond != nil && failedCond.Status == metav1.ConditionTrue {
		volume.Status.SetBuildArtifactSyncStatus(v1alpha1.ResourceRef(resourceName), applicationBuild.ShortBuildSrcHashFromSpec(), v1alpha1.BuildArtifactSyncStatusFailed)
		return nil
	}
	volume.Status.SetBuildArtifactSyncStatus(v1alpha1.ResourceRef(resourceName), applicationBuild.ShortBuildSrcHashFromSpec(), v1alpha1.BuildArtifactSyncStatusPending)
	return nil
}

func applicationBuildAvailable(applicationBuild *buildsv1alpha1.WorkspaceApplicationBuild) bool {
	availableCond := meta.FindStatusCondition(applicationBuild.Status.Conditions, string(buildsv1alpha1.WorkspaceApplicationBuildAvailable))
	if availableCond != nil && availableCond.Status == metav1.ConditionTrue {
		return true
	}
	return false
}

func buildArtifactSrcsGroupedByResource(volume *v1alpha1.WorkspaceVolume) map[string][]*v1alpha1.BuildArtifactSource {
	res := make(map[string][]*v1alpha1.BuildArtifactSource)
	for _, artifact := range volume.Spec.Source.BuildArtifacts {
		if _, ok := res[artifact.ResourceRef.String()]; !ok {
			res[artifact.ResourceRef.String()] = make([]*v1alpha1.BuildArtifactSource, 0)
		}
		res[artifact.ResourceRef.String()] = append(res[artifact.ResourceRef.String()], &artifact)
	}
	return res
}

func (r *WorkspaceVolumeReconciler) getApplicationBuildsForResources(ctx context.Context, volume *v1alpha1.WorkspaceVolume) (map[string]*buildsv1alpha1.WorkspaceApplicationBuild, error) {
	res := make(map[string]*buildsv1alpha1.WorkspaceApplicationBuild)
	for _, artifact := range volume.Spec.Source.BuildArtifacts {
		resourceRef := artifact.ResourceRef
		resource := &v1alpha1.WorkspaceResource{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: resourceRef.String(), Namespace: volume.Namespace}, resource); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, err
			}
			return nil, fmt.Errorf("failed to get the resource '%s' in volume '%s': %w", resourceRef, volume.Name, err)
		}
		applicationBuildName := buildsv1alpha1.ApplicationBuildName(resource)
		applicationBuild := &buildsv1alpha1.WorkspaceApplicationBuild{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: applicationBuildName, Namespace: volume.Namespace}, applicationBuild); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, err
			}
			return nil, fmt.Errorf("failed to get the application build '%s' for volume sync'%s': %w", applicationBuildName, volume.Name, err)
		}
		res[resourceRef.String()] = applicationBuild
	}
	return res, nil
}

func findJobCompleteCondition(job *batchv1.Job) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == batchv1.JobComplete {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

func shouldSkipVolumeSourceReconcile(volume *v1alpha1.WorkspaceVolume) bool {
	if volume.Spec.Source == nil {
		return true
	}
	return false
}
