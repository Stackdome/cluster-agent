package workspaceresource

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

type workloadDependencyChecker struct {
	client.Client
}

func (w *workloadDependencyChecker) DependenciesAvailable(ctx context.Context, resource *v1alpha1.WorkspaceResource) (bool, string, error) {
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
		if !workspaceAvailable(&currentDep) {
			return false, "Some dependency resources are not yet ready", nil
		}
	}
	return true, "", nil
}

func (w *workloadDependencyChecker) VolumeMountsReadyForUse(ctx context.Context, resource *v1alpha1.WorkspaceResource) (bool, string, error) {
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

func (w *workloadDependencyChecker) isVolumeReady(volume *v1alpha1.WorkspaceVolume, resource *v1alpha1.WorkspaceResource) (bool, string) {
	switch {
	case volume.Spec.Source == nil:
		return true, ""
	case volume.Spec.Source.LocalDir != nil:
		if !localDirSyncedToVolume(volume) {
			return false, "Local directory not yet synced to volume"
		}
	case volume.Spec.Source.BuildArtifacts != nil:
		if resource.Status.CurrentBuild == nil || !resource.Status.CurrentBuild.Available {
			return false, "Current build not yet available"
		}
		currentBuildID := resource.Status.CurrentBuild.ShortHash
		buildSyncStatus, found := volume.Status.BuildArtifactSyncs[v1alpha1.ResourceRef(resource.Name)]
		if !found || !buildSyncCompletedAndUptoDate(buildSyncStatus, currentBuildID) {
			return false, "Volume sync from build not yet complete"
		}
	}
	return true, ""
}

func localDirSyncedToVolume(volume *v1alpha1.WorkspaceVolume) bool {
	syncedOnceStatusCondition := meta.FindStatusCondition(volume.Status.Conditions, string(v1alpha1.WorkspaceVolumeConditionSyncedOnce))
	return syncedOnceStatusCondition != nil && syncedOnceStatusCondition.Status == metav1.ConditionTrue
}

func buildSyncCompletedAndUptoDate(buildSyncInfo v1alpha1.BuildArtifactSyncInfo, currentBuildID string) bool {
	return buildSyncInfo.Status == v1alpha1.BuildArtifactSyncStatusCompleted && buildSyncInfo.BuildID == currentBuildID
}

func (w *workloadDependencyChecker) getWorkspaceVolumes(ctx context.Context, resource *v1alpha1.WorkspaceResource) ([]*v1alpha1.WorkspaceVolume, error) {
	volumeRefs := make([]string, 0)
	for _, volumeMount := range resource.Spec.VolumeMounts {
		volumeRefs = append(volumeRefs, volumeMount.SourceWorkspaceVolume)
	}
	volumeList := &v1alpha1.WorkspaceVolumeList{}
	if err := w.Client.List(ctx, volumeList, client.InNamespace(resource.Namespace)); err != nil {
		return nil, err
	}
	filteredVolumeList := make([]*v1alpha1.WorkspaceVolume, 0)
	for _, volume := range volumeList.Items {
		if slices.Contains(volumeRefs, volume.Name) {
			filteredVolumeList = append(filteredVolumeList, &volume)
		}
	}
	return controller.Unique(filteredVolumeList), nil
}

func (w *workloadDependencyChecker) getDependencies(ctx context.Context, resource *v1alpha1.WorkspaceResource) ([]v1alpha1.WorkspaceResource, error) {
	if len(resource.Spec.DependsOn) == 0 {
		return nil, nil
	}
	wrList := &v1alpha1.WorkspaceResourceList{}
	workspaceRef := metav1.GetControllerOf(resource)
	if workspaceRef == nil {
		return nil, fmt.Errorf("missing owner ref for workspace resource")
	}
	if err := w.Client.List(ctx, wrList, client.InNamespace(resource.Namespace), client.MatchingFields{
		ownerKey: workspaceRef.Name,
	}); err != nil {
		return nil, err
	}

	res := make([]v1alpha1.WorkspaceResource, 0)
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
