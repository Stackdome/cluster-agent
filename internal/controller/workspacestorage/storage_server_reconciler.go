package workspacestorage

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

const (
	WorkspaceStorageLabel = "storage.stackdome.io/workspace"
)

type storageServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func WorkspaceStorageLabels(workspaceStorage *workspacev1alpha1.WorkspaceStorage) map[string]string {
	return map[string]string{WorkspaceStorageLabel: workspaceStorage.Spec.WorkspaceName}
}

func (r *storageServerReconciler) reconcile(ctx context.Context, workspaceStorage *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling storage server")
	return r.ensureStorageServerDeployment(ctx, workspaceStorage)
}

func (r *storageServerReconciler) ensureStorageServerDeployment(
	ctx context.Context,
	workspaceStorage *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, error) {
	volumes := make([]corev1.Volume, 0)
	wsVolumes, err := r.getWSVolumes(ctx, workspaceStorage)
	if err != nil {
		return resultNil, err
	}
	volumeMountsOnPod := make([]corev1.VolumeMount, 0)
	for _, volume := range wsVolumes {
		volumes = append(volumes,
			corev1.Volume{
				Name: volume.Name,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: volume.Status.PvcName,
					},
				},
			},
		)
		volumeMountsOnPod = append(
			volumeMountsOnPod,
			corev1.VolumeMount{
				Name:      volume.Name,
				MountPath: workspaceStorage.MountPathForVolume(volume.Name),
			},
		)
	}
	// Mount user's ssh public key to the storage pod.
	volumes = append(volumes, corev1.Volume{
		Name: "user-ssh-public-key",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: StorageServerSSHSecretName(workspaceStorage),
			},
		},
	})

	volumeMountsOnPod = append(volumeMountsOnPod, corev1.VolumeMount{
		Name:      "user-ssh-public-key",
		MountPath: "/home/stackdomeuser/.ssh/authorized_keys",
		SubPath:   "authorized_keys",
	})

	desiredDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workspaceStorage.Name,
			Namespace: workspaceStorage.Namespace,
			Labels:    WorkspaceStorageLabels(workspaceStorage),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: WorkspaceStorageLabels(workspaceStorage),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: WorkspaceStorageLabels(workspaceStorage),
				},
				Spec: corev1.PodSpec{
					Volumes: volumes,
					Containers: []corev1.Container{
						{
							Name: fmt.Sprintf("%s-storage-server", workspaceStorage.Name),
							// TODO: Change
							Image:           "docker.io/ashishmax31327/storage-server:1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 2222,
								},
							},
							VolumeMounts: volumeMountsOnPod,
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("1000Mi"),
								},
							},
						},
					},
				},
			},
		},
	}
	desiredDeployment.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))

	if err := controllerutil.SetControllerReference(workspaceStorage, desiredDeployment, r.Scheme); err != nil {
		return resultNil, err
	}

	existingDeployment := &appsv1.Deployment{}

	err = r.Client.Get(ctx, types.NamespacedName{Namespace: desiredDeployment.Namespace, Name: desiredDeployment.Name}, existingDeployment)
	if err != nil {
		if apierrors.IsNotFound(err) {
			reportWorkspaceStorageUnAvailable(workspaceStorage, "StorageServerNotReady", "Storage Server is being created")
			return resultRequeue, r.Client.Create(ctx, desiredDeployment)
		}
		return resultNil, err
	}
	logger := controller.LoggerFromContext(ctx)
	logger.Info("server side patching deployment")
	// Server side apply the deployment.
	if err := r.Client.Patch(ctx, desiredDeployment, client.Apply, &client.PatchOptions{
		Force:        ptr.To(true),
		FieldManager: WorkspaceStorageControllerName,
	}); err != nil {
		logger.Error(err, "failed to patch deployment", "error", err)
		return resultNil, err
	}

	logger.Info("server side patch completed")

	if controller.DeploymentAvailable(existingDeployment) {
		return resultNil, nil
	}

	reportWorkspaceStorageUnAvailable(workspaceStorage, "StorageServerNotReady", "Storage Server unavailable")
	return resultStop, nil
}

func (r *storageServerReconciler) getWSVolumes(ctx context.Context, ws *workspacev1alpha1.WorkspaceStorage) ([]workspacev1alpha1.WorkspaceVolume, error) {
	WSVolumeList := &workspacev1alpha1.WorkspaceVolumeList{}
	if err := r.Client.List(ctx, WSVolumeList, client.InNamespace(ws.Namespace), client.MatchingFields{
		ownerKey: ws.Name,
	}); err != nil {
		return nil, err
	}
	res := []workspacev1alpha1.WorkspaceVolume{}
	for _, volume := range WSVolumeList.Items {
		if ws.ContainsVolume(volume.Name) {
			res = append(res, volume)
		}
	}
	return res, nil
}
