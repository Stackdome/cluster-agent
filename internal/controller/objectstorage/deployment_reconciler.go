package objectstorage

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

type deploymentReconciler struct {
	client client.Client
	scheme *runtime.Scheme
	image  string
}

func newDeploymentReconciler(c client.Client, scheme *runtime.Scheme, image string) *deploymentReconciler {
	return &deploymentReconciler{client: c, scheme: scheme, image: image}
}

func (r *deploymentReconciler) name() string { return "deployment-reconciler" }

func (r *deploymentReconciler) reconcile(ctx context.Context, resource *storagev1alpha1.ObjectStorage) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)

	labels := map[string]string{"app": resource.DeploymentName()}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resource.DeploymentName(),
			Namespace: resource.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.client, deployment, func() error {
		deployment.Spec.Replicas = ptr.To(int32(1))
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		templateAnnotations := map[string]string{}
		if resource.Status.LastCredentialRotationTime != nil {
			templateAnnotations["objectstorage.stackdome.io/credentials-rotated-at"] = resource.Status.LastCredentialRotationTime.Format("2006-01-02T15:04:05Z")
		}
		deployment.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: templateAnnotations},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "rustfs",
						Image: r.image,
						Ports: []corev1.ContainerPort{
							{Name: "s3", ContainerPort: storagev1alpha1.ObjectStorageContainerPort, Protocol: corev1.ProtocolTCP},
						},
						EnvFrom: []corev1.EnvFromSource{
							{
								SecretRef: &corev1.SecretEnvSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: resource.CredentialsSecretName(),
									},
								},
							},
						},
						Resources: resourcesForSpec(resource),
						VolumeMounts: []corev1.VolumeMount{
							{Name: "data", MountPath: "/data"},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: resource.Status.PVCName,
							},
						},
					},
				},
			},
		}
		return controllerutil.SetControllerReference(resource, deployment, r.scheme)
	})
	if err != nil {
		return resultNil, err
	}

	logger.Info("ObjectStorage Deployment reconciled", "operation", op)

	if !controller.DeploymentAvailable(deployment) || !deploymentRolloutComplete(deployment) {
		logger.Info("ObjectStorage Deployment not ready", "name", deployment.Name,
			"available", controller.DeploymentAvailable(deployment),
			"updatedReplicas", deployment.Status.UpdatedReplicas,
			"readyReplicas", deployment.Status.ReadyReplicas,
			"desiredReplicas", *deployment.Spec.Replicas)
		setStatusCondition(resource, storagev1alpha1.ObjectStorageConditionAvailable, metav1.ConditionFalse, "DeploymentNotReady", fmt.Sprintf("ObjectStorage deployment %s is not yet available", deployment.Name))
		setPhase(resource, storagev1alpha1.ObjectStoragePhasePending)
		return resultStop, nil
	}

	return resultNil, nil
}

func deploymentRolloutComplete(deployment *appsv1.Deployment) bool {
	if deployment.Spec.Replicas == nil {
		return false
	}
	desired := *deployment.Spec.Replicas
	return deployment.Status.UpdatedReplicas == desired &&
		deployment.Status.ReadyReplicas == desired &&
		deployment.Status.AvailableReplicas == desired
}

func resourcesForSpec(resource *storagev1alpha1.ObjectStorage) corev1.ResourceRequirements {
	if resource.Spec.Resources != nil {
		return *resource.Spec.Resources
	}
	return corev1.ResourceRequirements{}
}
