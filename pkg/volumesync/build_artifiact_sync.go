package volumesync

import (
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller/workspaceconfiguration"
)

const (
	BinariesMountPath = "/binaries"
	BusyboxBinary     = "busybox"
	RsyncBinary       = "rsync"
	ShellCommand      = "sh"
	ShellFlag         = "-c"
	CopyCommand       = "cp"
	RecursiveCopyFlag = "-r"
	RemoveCommand     = "rm"
	RecursiveFlag     = "-rf"
)

func CreateBuildArtifactsVolumeSyncJob(
	volume *v1alpha1.WorkspaceVolume, buildSrcs []*v1alpha1.BuildArtifactSource,
	wab *v1alpha1.WorkspaceApplicationBuild) *batchv1.Job {
	volumes, volumeMounts := createVolumesAndMounts(volume, buildSrcs)
	copyCommands := generateCopyCommands(volume, buildSrcs)
	job := batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-volume-sync-%s", volume.Name, wab.ShortBuildSrcHashFromStatus()),
			Namespace: wab.Namespace,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:         "volume-initialization",
							Image:        wab.Status.ImageUrl,
							Command:      []string{fmt.Sprintf("%s/%s", BinariesMountPath, BusyboxBinary), ShellCommand, ShellFlag},
							Args:         []string{strings.Join(copyCommands, " && ")},
							VolumeMounts: volumeMounts,
						},
					},
					Volumes:       volumes,
					RestartPolicy: corev1.RestartPolicyOnFailure,
				},
			},
		},
	}
	return &job
}

func createVolumesAndMounts(volume *v1alpha1.WorkspaceVolume, buildSrcs []*v1alpha1.BuildArtifactSource) ([]corev1.Volume, []corev1.VolumeMount) {
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount
	for _, spec := range buildSrcs {
		volumes = append(
			volumes,
			corev1.Volume{
				Name: volume.Name,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: volume.Status.PvcName,
					},
				},
			},
			// Helper binaries pvc
			corev1.Volume{
				Name: "binaries",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: workspaceconfiguration.BusyBoxPVCName(),
					},
				},
			},
		)
		volumeMounts = append(
			volumeMounts,
			corev1.VolumeMount{
				Name:      volume.Name,
				MountPath: mountPathInContainer(volume.Name),
				SubPath:   spec.DestinationPath,
			},
			corev1.VolumeMount{
				Name:      "binaries",
				MountPath: BinariesMountPath,
			},
		)
	}
	return volumes, volumeMounts
}

func generateCopyCommands(volume *v1alpha1.WorkspaceVolume, buildSrcs []*v1alpha1.BuildArtifactSource) []string {
	var commands []string
	for _, spec := range buildSrcs {
		commands = append(commands, generateRsyncCommand(spec.SourcePath, mountPathInContainer(volume.Name)))
	}
	return commands
}

// TODO: Handle the case where the source is not a directory.
func generateRsyncCommand(src, dst string) string {
	if !strings.HasSuffix(src, "/") {
		src += "/"
	}
	return fmt.Sprintf("%s/%s -az --delete --progress --whole-file %s %s", BinariesMountPath, RsyncBinary, src, dst)
}

func mountPathInContainer(volumeName string) string {
	return fmt.Sprintf("/%s", volumeName)
}
