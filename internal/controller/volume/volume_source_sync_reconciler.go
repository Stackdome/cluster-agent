package volume

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
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"

	"stackdome.io/cluster-agent/pkg/volumesync"
)

const (
	ResourceNameLabel = "volumesync.stackdome.io/resourceName"
)

func (r *VolumeReconciler) reconcileVolumeSource(ctx context.Context, volume *storagev1alpha1.Volume) (subReconcilerResult, error) {
	if shouldSkipVolumeSourceReconcile(volume) {
		return resultNil, nil
	}

	subReconcilers := []func(context.Context, *storagev1alpha1.Volume) (subReconcilerResult, error){
		r.reconcileLocalVolumeSource,
		r.reconcileBuildArtifactsSources,
		r.reconcileGitSource,
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
func (r *VolumeReconciler) reconcileLocalVolumeSource(ctx context.Context, volume *storagev1alpha1.Volume) (subReconcilerResult, error) {
	if volume.Spec.Source.RemoteDir == nil {
		return resultNil, nil
	}
	localDirSrc := volume.Spec.Source.RemoteDir

	if len(localDirSrc.CurrentDirectoryHash) != 0 {
		meta.SetStatusCondition(&volume.Status.Conditions, metav1.Condition{
			Type:               string(storagev1alpha1.VolumeConditionSyncedFromRemote),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: volume.Generation,
			Message:            fmt.Sprintf("Directory with hash '%s' synced", localDirSrc.CurrentDirectoryHash),
			Reason:             "RemoteDataSynced",
		})
		volume.Status.LastRemoteSyncHash = localDirSrc.CurrentDirectoryHash
	}

	return resultNil, nil
}

func (r *VolumeReconciler) reconcileBuildArtifactsSources(ctx context.Context, volume *storagev1alpha1.Volume) (subReconcilerResult, error) {
	if volume.Spec.Source.BuildArtifacts == nil {
		return resultNil, nil
	}

	resourceBuildArtififactSrcsMap := buildArtifactSrcsGroupedByResource(volume)

	resourceBuildMap, err := r.getImageBuildsForResources(ctx, volume)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// We will get requeued when the application build/workspace resource is created.
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

func (r *VolumeReconciler) reconcileBuildArtifactsSrcsForResource(
	ctx context.Context,
	volume *storagev1alpha1.Volume,
	resourceName string,
	imageBuild *buildsv1alpha1.ImageBuild,
	buildArtifacts []*storagev1alpha1.BuildArtifactSource) error {
	if buildAvailable(imageBuild) {
		desiredJob := volumesync.CreateBuildArtifactsVolumeSyncJob(volume, buildArtifacts, imageBuild)
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
				storagev1alpha1.StackResourceReference{Name: resourceName},
				imageBuild.ShortBuildSrcRevisionFromStatus(),
				storagev1alpha1.BuildArtifactSyncStatusCompleted,
			)
			// TODO: Cleanup failed and completed jobs.
			return nil
		}
	}

	failedCond := meta.FindStatusCondition(imageBuild.Status.Conditions, string(buildsv1alpha1.BuildFailed))
	if failedCond != nil && failedCond.Status == metav1.ConditionTrue {
		volume.Status.SetBuildArtifactSyncStatus(
			storagev1alpha1.StackResourceReference{Name: resourceName},
			imageBuild.ShortBuildSrcRevisionFromSpec(),
			storagev1alpha1.BuildArtifactSyncStatusFailed,
		)
		return nil
	}
	volume.Status.SetBuildArtifactSyncStatus(
		storagev1alpha1.StackResourceReference{Name: resourceName},
		imageBuild.ShortBuildSrcRevisionFromSpec(),
		storagev1alpha1.BuildArtifactSyncStatusPending,
	)
	return nil
}

func buildAvailable(imageBuild *buildsv1alpha1.ImageBuild) bool {
	availableCond := meta.FindStatusCondition(imageBuild.Status.Conditions, string(buildsv1alpha1.BuildAvailable))
	if availableCond != nil && availableCond.Status == metav1.ConditionTrue {
		return true
	}
	return false
}

func buildArtifactSrcsGroupedByResource(volume *storagev1alpha1.Volume) map[string][]*storagev1alpha1.BuildArtifactSource {
	res := make(map[string][]*storagev1alpha1.BuildArtifactSource)
	for _, artifact := range volume.Spec.Source.BuildArtifacts {
		if _, ok := res[artifact.BuildSource.Name]; !ok {
			res[artifact.BuildSource.Name] = make([]*storagev1alpha1.BuildArtifactSource, 0)
		}
		res[artifact.BuildSource.Name] = append(res[artifact.BuildSource.Name], &artifact)
	}
	return res
}

func (r *VolumeReconciler) getImageBuildsForResources(ctx context.Context, volume *storagev1alpha1.Volume) (map[string]*buildsv1alpha1.ImageBuild, error) {
	res := make(map[string]*buildsv1alpha1.ImageBuild)
	for _, artifact := range volume.Spec.Source.BuildArtifacts {
		stackresourceRef := artifact.BuildSource.Name
		resource := &v1alpha1.StackResource{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: stackresourceRef, Namespace: volume.Namespace}, resource); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, err
			}
			return nil, fmt.Errorf("failed to get the resource '%s' in volume '%s': %w", stackresourceRef, volume.Name, err)
		}
		imageBuildName := buildsv1alpha1.ImageBuildName(resource.Name, resource.Spec.BuildSpec.SourceRevision.GetSourceRevisionString())
		imageBuild := &buildsv1alpha1.ImageBuild{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: imageBuildName, Namespace: volume.Namespace}, imageBuild); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, err
			}
			return nil, fmt.Errorf("failed to get the application build '%s' for volume sync'%s': %w", imageBuildName, volume.Name, err)
		}
		res[stackresourceRef] = imageBuild
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

func shouldSkipVolumeSourceReconcile(volume *storagev1alpha1.Volume) bool {
	return volume.Spec.Source == nil
}
