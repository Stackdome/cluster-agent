package stackstorage

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
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/pkg/config"

	"stackdome.io/cluster-agent/internal/controller"
)

const (
	storageLabel = "storage.stackdome.io/ProvisionedFor"
)

type sshServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func storageLabels(storage *storagev1alpha1.Storage) map[string]string {
	return map[string]string{
		storageLabel:                          storage.Spec.ProvisionedFor,
		"storage.stackdome.io/ssh-server-for": storage.Name,
	}
}

func (r *sshServerReconciler) reconcile(ctx context.Context, storage *storagev1alpha1.Storage) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling storage server")
	return r.ensureStorageServerDeployment(ctx, storage)
}

func (r *sshServerReconciler) ensureStorageServerDeployment(
	ctx context.Context,
	storage *storagev1alpha1.Storage) (subReconcilerResult, error) {
	definedVolumes, err := r.getVolumesDefined(ctx, storage)
	if err != nil {
		return resultNil, err
	}
	volumeMountsOnPod := make([]corev1.VolumeMount, 0)
	volumesToBeMounted := make([]corev1.Volume, 0)
	for _, volume := range definedVolumes {
		volumesToBeMounted = append(volumesToBeMounted,
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
				MountPath: storage.MountPathForVolume(volume.Name),
			},
		)
	}
	// Mount user's ssh public key to the storage pod.
	volumesToBeMounted = append(volumesToBeMounted, corev1.Volume{
		Name: "user-ssh-public-key",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: StorageServerSSHSecretName(storage),
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
			Name:      storage.Name,
			Namespace: storage.Namespace,
			Labels:    storageLabels(storage),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: storageLabels(storage),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: storageLabels(storage),
				},
				Spec: corev1.PodSpec{
					Volumes: volumesToBeMounted,
					Containers: []corev1.Container{
						{
							Name: fmt.Sprintf("%s-storage-server", storage.Name),
							// TODO: Change
							Image:           config.StackdomeSSHServerImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 2222,
								},
							},
							VolumeMounts: volumeMountsOnPod,
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
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

	if err := controllerutil.SetControllerReference(storage, desiredDeployment, r.Scheme); err != nil {
		return resultNil, err
	}

	existingDeployment := &appsv1.Deployment{}

	err = r.Client.Get(ctx, types.NamespacedName{Namespace: desiredDeployment.Namespace, Name: desiredDeployment.Name}, existingDeployment)
	if err != nil {
		if apierrors.IsNotFound(err) {
			reportStorageUnAvailable(storage, "StorageServerNotReady", "Storage Server is being created")
			return resultRequeue, r.Client.Create(ctx, desiredDeployment)
		}
		return resultNil, err
	}
	logger := controller.LoggerFromContext(ctx)
	logger.Info("server side patching deployment")
	// Server side apply the deployment.
	if err := r.Client.Patch(ctx, desiredDeployment, client.Apply, &client.PatchOptions{
		Force:        ptr.To(true),
		FieldManager: StorageControllerName,
	}); err != nil {
		logger.Error(err, "failed to patch deployment", "error", err)
		return resultNil, err
	}

	logger.Info("server side patch completed")

	if controller.DeploymentAvailable(existingDeployment) {
		return resultNil, nil
	}

	reportStorageUnAvailable(storage, "StorageServerNotReady", "Storage Server unavailable")
	return resultStop, nil
}

func (r *sshServerReconciler) getVolumesDefined(ctx context.Context, storage *storagev1alpha1.Storage) ([]storagev1alpha1.Volume, error) {
	volumeList := &storagev1alpha1.VolumeList{}
	if err := r.Client.List(ctx, volumeList, client.InNamespace(storage.Namespace), client.MatchingFields{
		ownerKey: storage.Name,
	}); err != nil {
		return nil, err
	}
	res := []storagev1alpha1.Volume{}
	for _, volume := range volumeList.Items {
		if storage.ContainsVolume(volume.Name) {
			res = append(res, volume)
		}
	}
	return res, nil
}
