package workspacestate

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"soradev.io/cluster-agent/api/v1alpha1"
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
	"soradev.io/cluster-agent/pkg/rsync"
)

func StoragePodLabels(workspaceState *workspacev1alpha1.WorkspaceState) map[string]string {
	return map[string]string{"app": workspaceState.Name}
}

func (r *WorkspaceStateReconciler) reconcileStoragePods(ctx context.Context, workspaceState *workspacev1alpha1.WorkspaceState) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling storage pods")
	rsyncConf, err := r.ensureRsyncConfConfigMap(ctx, workspaceState)
	if err != nil {
		return resultNil, fmt.Errorf("failed to create rsync-conf config maps: %w", err)
	}

	return r.ensureRsyncServerDeployment(ctx, workspaceState, rsyncConf)
}

func (r *WorkspaceStateReconciler) ensureRsyncServerDeployment(ctx context.Context, workspaceState *workspacev1alpha1.WorkspaceState, rsyncConf rsync.RsyncConf) (subReconcilerResult, error) {

	volumes := make([]corev1.Volume, 0)

	// TODO: fix this later. Fetch volumes from the cache/api-server

	for _, resource := range workspaceState.Spec.Resources {
		volumes = append(volumes, corev1.Volume{
			Name: resource.Name,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: resource.Name,
				},
			},
		})
	}

	resourceStateMounts := make([]corev1.VolumeMount, 0)

	for _, moduleConf := range rsyncConf {
		resourceStateMounts = append(
			resourceStateMounts,
			corev1.VolumeMount{
				Name:      moduleConf.ModuleName,
				MountPath: moduleConf.Path,
			},
		)
	}

	rsyncConfVolumeName := fmt.Sprintf("%s-rsync-conf", workspaceState.Name)

	desiredDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workspaceState.Name,
			Namespace: workspaceState.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: StoragePodLabels(workspaceState),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: StoragePodLabels(workspaceState),
				},
				Spec: corev1.PodSpec{
					Volumes: append(volumes, corev1.Volume{
						Name: rsyncConfVolumeName,
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: workspaceState.Name},
							},
						},
					}),
					Containers: []corev1.Container{
						{
							Name: fmt.Sprintf("%s-storage-server", workspaceState.Name),
							// TODO: Change
							Image:           "rsync-daemon:1",
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

	if err := controllerutil.SetControllerReference(workspaceState, desiredDeployment, r.Scheme); err != nil {
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

	specChanged := !equality.Semantic.DeepEqual(desiredDeployment.Spec, existingDeployment.Spec)
	if specChanged {
		existingDeployment.Spec = desiredDeployment.Spec
		return resultNil, r.Client.Update(ctx, existingDeployment)
	}

	if checkDeploymentCondition(existingDeployment, appsv1.DeploymentAvailable, corev1.ConditionTrue) {
		return resultNil, nil
	}
	r.reportWorkspaceStateUnready("Storage Server unready", workspaceState)
	return resultRequeue, nil
}

func (r *WorkspaceStateReconciler) ensureRsyncConfConfigMap(ctx context.Context, workspaceState *workspacev1alpha1.WorkspaceState) (rsync.RsyncConf, error) {
	rsyncConf := generateRsyncConfs(workspaceState)
	config, err := rsyncConf.GenerateRsyncConfFile()
	if err != nil {
		return nil, fmt.Errorf("failed to generate rsync conf file: %w", err)
	}
	desiredCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workspaceState.Name,
			Namespace: workspaceState.Namespace,
		},
		Data: map[string]string{
			"rsyncd.conf": config,
		},
	}

	if err := controllerutil.SetOwnerReference(workspaceState, desiredCM, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set owner refs: %w", err)
	}

	existingCM := &corev1.ConfigMap{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: desiredCM.Name, Namespace: desiredCM.Namespace}, existingCM); err != nil {
		if apierrors.IsNotFound(err) {
			return rsyncConf, r.Client.Create(ctx, desiredCM)
		}
		return nil, err
	}

	dataChanged := !equality.Semantic.DeepEqual(existingCM.Data, desiredCM.Data)
	if dataChanged {
		existingCM.Data = desiredCM.Data
		return rsyncConf, r.Client.Update(ctx, existingCM)
	}
	return rsyncConf, nil
}

func generateRsyncConfs(workspaceState *workspacev1alpha1.WorkspaceState) rsync.RsyncConf {
	rsyncModules := make([]rsync.RsyncConfigModule, 0)

	for i := range workspaceState.Spec.Resources {
		resource := &workspaceState.Spec.Resources[i]
		rsyncModules = append(
			rsyncModules,
			rsync.NewRsyncModuleConfig(
				rsync.RsyncConfigModuleSpec{
					ModuleName: resource.Name,
					// TODO: prefix path with the user name.
					Path:      fmt.Sprintf("/%s/%s", workspaceState.Name, resource.Name),
					HostAllow: "*",
				},
			),
		)
	}

	return rsync.NewRsyncConf(rsyncModules...)
}

func checkDeploymentCondition(deployment *appsv1.Deployment, conditionType appsv1.DeploymentConditionType, status corev1.ConditionStatus) bool {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == conditionType && condition.Status == status {
			return true
		}
	}
	return false
}

func (r *WorkspaceStateReconciler) reportWorkspaceStateUnready(msg string, workspaceState *v1alpha1.WorkspaceState) {
	meta.SetStatusCondition(&workspaceState.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceStateConditionAvailable),
		Status:             metav1.ConditionFalse,
		Reason:             msg,
		ObservedGeneration: workspaceState.Generation,
	})
}
