package objectstorage

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

type serviceReconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

func newServiceReconciler(c client.Client, scheme *runtime.Scheme) *serviceReconciler {
	return &serviceReconciler{client: c, scheme: scheme}
}

func (r *serviceReconciler) name() string { return "service-reconciler" }

func (r *serviceReconciler) reconcile(ctx context.Context, resource *storagev1alpha1.ObjectStorage) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)

	desiredService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resource.ServiceName(),
			Namespace: resource.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": resource.DeploymentName()},
			Ports: []corev1.ServicePort{
				{
					Name:     "s3",
					Port:     storagev1alpha1.ObjectStorageContainerPort,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(resource, desiredService, r.scheme); err != nil {
		return resultNil, err
	}

	existingService := &corev1.Service{}
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(desiredService), existingService); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating Service", "name", desiredService.Name)
			return resultRequeue, r.client.Create(ctx, desiredService)
		}
		return resultNil, err
	}

	resource.Status.Endpoint = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", existingService.Name, existingService.Namespace, storagev1alpha1.ObjectStorageContainerPort)
	return resultNil, nil
}
