package workspacestorage

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

type storageServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func WorkspaceStorageLabels(workspaceStorage *workspacev1alpha1.WorkspaceStorage) map[string]string {
	return map[string]string{"storageFor": workspaceStorage.Name}
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
	logger := controller.LoggerFromContext(ctx)
	for _, volume := range wsVolumes {
		volumes = append(volumes, corev1.Volume{
			Name: volume.Name,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: volume.Status.PvcName,
				},
			},
		})
		volumeMountsOnPod = append(
			volumeMountsOnPod,
			corev1.VolumeMount{
				Name:      volume.Name,
				MountPath: workspaceStorage.MountPathForVolume(volume.Name),
			},
		)
	}

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
							Image:           "myregistry.localhost:5000/rsync-daemon:1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 22,
								},
							},
							VolumeMounts: volumeMountsOnPod,
						},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(workspaceStorage, desiredDeployment, r.Scheme); err != nil {
		return resultNil, err
	}

	existingDeployment := &appsv1.Deployment{}

	err = r.Client.Get(ctx, types.NamespacedName{Namespace: desiredDeployment.Namespace, Name: desiredDeployment.Name}, existingDeployment)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, desiredDeployment)
		}
		return resultNil, err
	}

	specChanged := !equality.Semantic.DeepDerivative(desiredDeployment.Spec, existingDeployment.Spec)
	if specChanged {
		logger.Info("Updating storage server deployment reconciler")
		existingDeployment.Spec = desiredDeployment.Spec
		return resultRequeue, r.Client.Update(ctx, existingDeployment)
	}

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
