package volume

import (
	"context"
	"crypto/sha256"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/pkg/gitsync"
)

const (
	// This is where the git repo contents will lie within the volume.
	DestDirWithinVolume = "repo"
)

func gitSyncJobName(volumeName, revision string) string {
	hash := sha256.Sum256([]byte(revision))
	shortHash := fmt.Sprintf("%x", hash)[:8]
	name := fmt.Sprintf("volume-%s-git-sync-%s", volumeName, shortHash)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

func (r *VolumeReconciler) reconcileGitSource(ctx context.Context, volume *storagev1alpha1.Volume) (subReconcilerResult, error) {
	if volume.Spec.Source.GitRepo == nil {
		return resultNil, nil
	}

	if len(volume.Status.PvcName) == 0 {
		return resultRequeue, nil
	}

	gitSource := volume.Spec.Source.GitRepo
	params, err := gitsync.BuildGitSyncParams(volume, DestDirWithinVolume)
	if err != nil {
		return resultNil, fmt.Errorf("failed to build git sync job params: %v", err)
	}

	jobName := gitSyncJobName(volume.Name, gitSource.Revision.GetGitRevisionString())

	desiredJob, err := gitsync.GenerateGitSyncJob(jobName, params)
	if err != nil {
		return resultNil, fmt.Errorf("failed to build git sync job: %v", err)
	}

	if err := ctrl.SetControllerReference(volume, desiredJob, r.Scheme); err != nil {
		return resultNil, err
	}
	existingJob := &batchv1.Job{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(desiredJob), existingJob); err != nil {
		if apierrors.IsNotFound(err) {
			return resultNil, r.Client.Create(ctx, desiredJob)
		}
		return resultNil, fmt.Errorf("failed to get existing git sync job: %w", err)
	}

	jobcompletedCond := findJobCompleteCondition(existingJob)
	if jobcompletedCond != nil && jobcompletedCond.Status == corev1.ConditionTrue {
		meta.SetStatusCondition(&volume.Status.Conditions, metav1.Condition{
			Type:               string(storagev1alpha1.VolumeConditionSyncedFromGitSource),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: volume.Generation,
			Message:            "Volume has been successfully synced from git source",
			Reason:             "GitSyncComplete",
		})
		volume.Status.GitRepoSyncedPathWithinVolume = DestDirWithinVolume
		volume.Status.LastSyncedGitReference = gitSource.Revision.GetGitRevisionString()
	} else {
		meta.SetStatusCondition(&volume.Status.Conditions, metav1.Condition{
			Type:               string(storagev1alpha1.VolumeConditionSyncedFromGitSource),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: volume.Generation,
			Message:            "Volume sync from git source not complete",
			Reason:             "GitSyncNotCompleteComplete",
		})
	}
	return resultNil, r.Client.Status().Update(ctx, volume)
}

// apiVersion: v1
// kind: Pod
// metadata:
//   name: nginx-infisical
// spec:
//   containers:
//   - name: nginx
//     image: nginx:latest
//     ports:
//     - containerPort: 80
//     volumeMounts:
//     - name: infisical-source-volume
//       mountPath: /usr/share/nginx/html
//       subPath: repo
//   volumes:
//   - name: infisical-source-volume
//     persistentVolumeClaim:
//       claimName: infisical-source
//   restartPolicy: Always
