package volumesync

import (
	"fmt"
	"path/filepath"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

const (
	SyncToolsImage    = "stackdome/sync-tools:latest"
	ToolsMountPath    = "/tools"
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
	volume *v1alpha1.WorkspaceVolume, copySrcs []*v1alpha1.BuildArtifactSource,
	imageBuild *buildsv1alpha1.ImageBuild) *batchv1.Job {
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      volume.Name,
			MountPath: mountPathInContainer(volume.Name),
		},
	}
	copyCommands := generateCopyCommands(volume, copySrcs)
	job := batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-volume-sync-%s", volume.Name, imageBuild.ShortBuildSrcHashFromStatus()),
			Namespace: imageBuild.Namespace,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					// Copy rsync and busy box first to a empty dir volume as an init step.
					InitContainers: []corev1.Container{
						{
							Name:    "sync-tools-setup",
							Image:   SyncToolsImage, // Custom minimal image with rsync and busybox
							Command: []string{"/bin/sh", "-c"},
							Args: []string{
								fmt.Sprintf(
									"cp /usr/bin/rsync %s/ && cp /bin/busybox %s/",
									ToolsMountPath,
									ToolsMountPath,
								),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "tools-dir",
									MountPath: ToolsMountPath,
								},
							},
						},
					},
					// We then mount the volume with the binaries copied to the build image
					// in a directory and then invoke rsync to copy the contents from the image
					// to the volume.
					Containers: []corev1.Container{
						{
							Name:    "volume-initialization",
							Image:   imageBuild.Status.ImageUrl,
							Command: []string{"/tools/busybox", "sh", "-c"},
							Args:    []string{fmt.Sprintf("PATH=$PATH:/tools %s", strings.Join(copyCommands, " && "))},
							VolumeMounts: append(volumeMounts, corev1.VolumeMount{
								Name:      "tools-dir",
								MountPath: ToolsMountPath,
							}),
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: volume.Name,
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: volume.Status.PvcName,
								},
							},
						},
						{
							Name: "tools-dir",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyOnFailure,
				},
			},
		},
	}
	return &job
}

func generateCopyCommands(volume *v1alpha1.WorkspaceVolume, copySrcs []*v1alpha1.BuildArtifactSource) []string {
	var commands []string
	for _, spec := range copySrcs {
		commands = append(commands, generateRsyncCommand(
			spec.SourcePath,
			filepath.Join(mountPathInContainer(volume.Name), spec.DestinationPath),
		))
	}
	return commands
}

// TODO: Handle the case where the source is not a directory.
func generateRsyncCommand(src, dst string) string {
	if !strings.HasSuffix(src, "/") {
		src += "/"
	}
	return fmt.Sprintf("%s/%s -az --mkpath --delete --progress --whole-file %s %s", ToolsMountPath, RsyncBinary, src, dst)
}

func mountPathInContainer(volumeName string) string {
	return fmt.Sprintf("/%s", volumeName)
}
