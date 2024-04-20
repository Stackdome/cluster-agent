package workspacestorage

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"soradev.io/cluster-agent/api/v1alpha1"
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
	"soradev.io/cluster-agent/pkg/rsync"
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
	err := r.ensureRsyncConfConfigMap(ctx, workspaceStorage)
	if err != nil {
		return resultNil, fmt.Errorf("failed to create rsync-conf configmap: %w", err)
	}

	return r.ensureRsyncServerDeployment(ctx, workspaceStorage)
}

func (r *storageServerReconciler) ensureRsyncServerDeployment(
	ctx context.Context,
	workspaceStorage *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, error) {
	volumes := make([]corev1.Volume, 0)
	resourceStateMounts := make([]corev1.VolumeMount, 0)
	logger := controller.LoggerFromContext(ctx)
	// TODO: fix this later. Fetch PVCs from the cache/api-server
	for _, resource := range workspaceStorage.Spec.ResourceStorageSpecs {
		volumes = append(volumes, corev1.Volume{
			Name: resource.Name,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: workspaceStorage.GeneratePVCName(&resource),
				},
			},
		})
		resourceStateMounts = append(
			resourceStateMounts,
			corev1.VolumeMount{
				Name:      resource.Name,
				MountPath: workspaceStorage.MountPathForResource(&resource),
			},
		)
	}

	rsyncConfVolumeName := fmt.Sprintf("%s-rsync-conf", workspaceStorage.Name)

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
					Volumes: append(volumes, corev1.Volume{
						Name: rsyncConfVolumeName,
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: workspaceStorage.Name},
							},
						},
					}),
					Containers: []corev1.Container{
						{
							Name: fmt.Sprintf("%s-storage-server", workspaceStorage.Name),
							// TODO: Change
							Image:           "myregistry.localhost:5000/rsync-daemon:1",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{
									// TODO: change listening port in rsync server.
									ContainerPort: 873,
								},
							},
							VolumeMounts: append(resourceStateMounts, corev1.VolumeMount{
								Name:      rsyncConfVolumeName,
								MountPath: "/rsync_conf/rsyncd.conf",
								SubPath:   "rsyncd.conf",
							}),
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

	err := r.Client.Get(ctx, types.NamespacedName{Namespace: desiredDeployment.Namespace, Name: desiredDeployment.Name}, existingDeployment)
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

	if controller.CheckDeploymentAvailable(existingDeployment) {
		return resultNil, nil
	}
	reportWorkspaceStorageUnAvailable(workspaceStorage, "StorageServerNotReady", "Storage Server unavailable")
	return resultStop, nil
}

func (r *storageServerReconciler) ensureRsyncConfConfigMap(ctx context.Context, workspaceStorage *workspacev1alpha1.WorkspaceStorage) error {
	rsyncConf := generateRsyncConfs(workspaceStorage)
	config, err := rsyncConf.GenerateRsyncConfFile()
	if err != nil {
		return fmt.Errorf("failed to generate rsync conf file: %w", err)
	}
	desiredCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workspaceStorage.Name,
			Namespace: workspaceStorage.Namespace,
		},
		Data: map[string]string{
			"rsyncd.conf": config,
		},
	}

	if err := controllerutil.SetOwnerReference(workspaceStorage, desiredCM, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner refs: %w", err)
	}

	existingCM := &corev1.ConfigMap{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: desiredCM.Name, Namespace: desiredCM.Namespace}, existingCM); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Client.Create(ctx, desiredCM)
		}
		return err
	}

	dataChanged := !equality.Semantic.DeepEqual(existingCM.Data, desiredCM.Data)
	if dataChanged {
		existingCM.Data = desiredCM.Data
		return r.Client.Update(ctx, existingCM)
	}
	return nil
}

func generateRsyncConfs(workspaceStorage *workspacev1alpha1.WorkspaceStorage) rsync.RsyncConf {
	rsyncModules := make([]rsync.RsyncConfigModule, 0)

	for i := range workspaceStorage.Spec.ResourceStorageSpecs {
		resource := &workspaceStorage.Spec.ResourceStorageSpecs[i]
		if !resource.DontAllowSync {
			rsyncModules = append(
				rsyncModules,
				rsync.NewRsyncModuleConfig(
					rsync.RsyncConfigModuleSpec{
						ModuleName: resource.Name,
						// TODO: prefix path with the user name.
						Path:      workspaceStorage.MountPathForResource(resource),
						HostAllow: "*",
					},
				),
			)
		}
	}

	return rsync.NewRsyncConf(rsyncModules...)
}

func reportWorkspaceStorageUnAvailable(workspaceStorage *v1alpha1.WorkspaceStorage, reason string, msg string) {
	workspaceStorage.Status.Phase = v1alpha1.WSPending
	meta.SetStatusCondition(&workspaceStorage.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceStorageAvailable),
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: workspaceStorage.Generation,
	})
}
