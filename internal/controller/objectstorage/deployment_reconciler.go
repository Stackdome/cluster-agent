package objectstorage

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	desiredDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resource.DeploymentName(),
			Namespace: resource.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "s3gw",
							Image: r.image,
							Ports: []corev1.ContainerPort{
								{ContainerPort: 7480, Protocol: corev1.ProtocolTCP},
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
									ClaimName: resource.Status.VolumeName,
								},
							},
						},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(resource, desiredDeployment, r.scheme); err != nil {
		return resultNil, err
	}

	existingDeployment := &appsv1.Deployment{}
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(desiredDeployment), existingDeployment); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating s3gw Deployment", "name", desiredDeployment.Name)
			return resultRequeue, r.client.Create(ctx, desiredDeployment)
		}
		return resultNil, err
	}

	if !equality.Semantic.DeepDerivative(desiredDeployment.Spec, existingDeployment.Spec) {
		desiredDeployment.ResourceVersion = existingDeployment.ResourceVersion
		if err := r.client.Update(ctx, desiredDeployment); err != nil {
			return resultNil, err
		}
	}

	if err := r.client.Get(ctx, client.ObjectKeyFromObject(desiredDeployment), existingDeployment); err != nil {
		return resultNil, err
	}

	if !controller.DeploymentAvailable(existingDeployment) {
		logger.Info("s3gw Deployment not available yet", "name", existingDeployment.Name)
		setStatusCondition(resource, storagev1alpha1.ObjectStorageConditionAvailable, metav1.ConditionFalse, "DeploymentNotReady", fmt.Sprintf("s3gw deployment %s is not yet available", existingDeployment.Name))
		setPhase(resource, storagev1alpha1.ObjectStoragePhasePending)
		return resultStop, nil
	}

	return resultNil, nil
}
