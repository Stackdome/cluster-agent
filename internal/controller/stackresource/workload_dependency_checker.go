package stackresource

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"

	"stackdome.io/cluster-agent/internal/controller"
)

type workloadDependencyChecker struct {
	client.Client
}

func (w *workloadDependencyChecker) DependenciesAvailable(ctx context.Context, resource *v1alpha1.StackResource) (bool, string, error) {
	if len(resource.Spec.DependsOn) == 0 {
		return true, "", nil
	}
	dependencyList, err := w.getDependencies(ctx, resource)
	if err != nil {
		return false, "", err
	}
	if len(dependencyList) != len(resource.Spec.DependsOn) {
		return false, "", fmt.Errorf("some dependency resource are not yet created")
	}

	for i := range dependencyList {
		currentDep := dependencyList[i]
		if !stackResourceAvailable(&currentDep) {
			return false, "Some dependency resources are not yet ready", nil
		}
	}
	return true, "", nil
}

func (w *workloadDependencyChecker) VolumeMountsReadyForUse(ctx context.Context, resource *v1alpha1.StackResource) (bool, string, error) {
	if len(resource.Spec.VolumeMounts) == 0 {
		return true, "", nil
	}

	volumes, err := w.getWorkspaceVolumes(ctx, resource)
	if err != nil {
		return false, "", fmt.Errorf("failed to get workspace volumes: %w", err)
	}

	for _, volume := range volumes {
		ready, message := w.isVolumeReady(volume, resource)
		if !ready {
			return false, message, nil
		}
	}

	return true, "", nil
}

func (w *workloadDependencyChecker) isVolumeReady(volume *storagev1alpha1.Volume, resource *v1alpha1.StackResource) (bool, string) {
	switch {
	case volume.Spec.Source == nil:
		return true, ""
	case volume.Spec.Source.LocalDir != nil:
		if !localDirSyncedToVolume(volume) {
			return false, "Local directory not yet synced to volume"
		}
	case volume.Spec.Source.GitRepo != nil:
		if !gitRepoSyncedToVolume(volume) {
			return false, "Git repository not yet synced to volume"
		}
	case volume.Spec.Source.BuildArtifacts != nil:
		if resource.Status.CurrentBuild == nil || !resource.Status.CurrentBuild.Available {
			return false, "Current build not yet available"
		}
		currentBuildID := resource.Status.CurrentBuild.SourceRevision
		buildSyncStatus, found := volume.Status.BuildArtifactSyncs[resource.Name]
		if !found || !buildSyncCompletedAndUptoDate(buildSyncStatus, currentBuildID) {
			return false, "Volume sync from build not yet complete"
		}
	}
	return true, ""
}

func localDirSyncedToVolume(volume *storagev1alpha1.Volume) bool {
	syncedStatusCondition := meta.FindStatusCondition(volume.Status.Conditions, string(storagev1alpha1.VolumeConditionSyncedFromRemote))
	conditionSatisfied := syncedStatusCondition != nil &&
		syncedStatusCondition.Status == metav1.ConditionTrue &&
		volume.Status.ObservedGeneration == volume.Generation

	syncedHashesMatch := volume.Status.LastRemoteSyncHash == volume.Spec.Source.LocalDir.CurrentDirectoryHash
	return conditionSatisfied && syncedHashesMatch
}

func gitRepoSyncedToVolume(volume *storagev1alpha1.Volume) bool {
	gitRepoSyncedCondition := meta.FindStatusCondition(volume.Status.Conditions, string(storagev1alpha1.VolumeConditionSyncedFromGitSource))
	conditionSatisfied := gitRepoSyncedCondition != nil &&
		gitRepoSyncedCondition.Status == metav1.ConditionTrue &&
		volume.Status.ObservedGeneration == volume.Generation
	gitRepoHashMatches := volume.Status.LastSyncedGitReference == volume.Spec.Source.GitRepo.Revision.GetGitRevisionString()
	return conditionSatisfied && gitRepoHashMatches
}

func buildSyncCompletedAndUptoDate(buildSyncInfo storagev1alpha1.BuildArtifactSyncInfo, currentBuildID string) bool {
	return buildSyncInfo.Status == storagev1alpha1.BuildArtifactSyncStatusCompleted && buildSyncInfo.BuildID == currentBuildID
}

func (w *workloadDependencyChecker) getWorkspaceVolumes(ctx context.Context, resource *v1alpha1.StackResource) ([]*storagev1alpha1.Volume, error) {
	volumeRefs := make([]string, 0)
	for _, volumeMount := range resource.Spec.VolumeMounts {
		volumeRefs = append(volumeRefs, volumeMount.SourceVolume)
	}
	volumeList := &storagev1alpha1.VolumeList{}
	if err := w.Client.List(ctx, volumeList, client.InNamespace(resource.Namespace)); err != nil {
		return nil, err
	}
	filteredVolumeList := make([]*storagev1alpha1.Volume, 0)
	for _, volume := range volumeList.Items {
		if slices.Contains(volumeRefs, volume.Name) {
			filteredVolumeList = append(filteredVolumeList, &volume)
		}
	}
	return controller.Unique(filteredVolumeList), nil
}

func (w *workloadDependencyChecker) getDependencies(ctx context.Context, resource *v1alpha1.StackResource) ([]v1alpha1.StackResource, error) {
	if len(resource.Spec.DependsOn) == 0 {
		return nil, nil
	}
	wrList := &v1alpha1.StackResourceList{}
	workspaceRef := metav1.GetControllerOf(resource)
	if workspaceRef == nil {
		return nil, fmt.Errorf("missing owner ref for workspace resource")
	}
	if err := w.Client.List(ctx, wrList, client.InNamespace(resource.Namespace), client.MatchingFields{
		ownerKey: workspaceRef.Name,
	}); err != nil {
		return nil, err
	}

	res := make([]v1alpha1.StackResource, 0)
	for _, wr := range wrList.Items {
		if slices.Contains(resource.Spec.DependsOn, wr.Name) {
			res = append(res, wr)
		}
	}
	if len(res) != len(resource.Spec.DependsOn) {
		return nil, fmt.Errorf("missing workspace resource deps")
	}
	return res, nil
}
