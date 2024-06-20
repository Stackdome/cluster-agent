package workspacestorage

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

// Reconciles the internal svc for the storage server.
type serviceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func StorageServerServiceName(workspaceStorage *workspacev1alpha1.WorkspaceStorage) string {
	return fmt.Sprintf("%s-ws-svc", workspaceStorage.Name)
}

func StorageServiceNamespacedName(workspaceStorage *workspacev1alpha1.WorkspaceStorage) types.NamespacedName {
	return types.NamespacedName{
		Name:      StorageServerServiceName(workspaceStorage),
		Namespace: workspaceStorage.Namespace,
	}
}

func (r *serviceReconciler) reconcile(ctx context.Context, workspaceStorage *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconcile svc")
	desiredSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      StorageServerServiceName(workspaceStorage),
			Namespace: workspaceStorage.Namespace,
			Labels:    WorkspaceStorageLabels(workspaceStorage),
		},
		Spec: corev1.ServiceSpec{
			Selector: WorkspaceStorageLabels(workspaceStorage),
			Ports: []corev1.ServicePort{
				{
					Name:       "ssh",
					Port:       2222,
					TargetPort: intstr.FromInt(2222),
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(workspaceStorage, desiredSvc, r.Scheme); err != nil {
		return resultNil, err
	}
	existingSvc := &corev1.Service{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      desiredSvc.Name,
		Namespace: desiredSvc.Namespace},
		existingSvc,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Create(ctx, desiredSvc)
		}
		return resultNil, err
	}
	if !controller.AreServicesEqual(desiredSvc, existingSvc) {
		existingSvc.Spec = desiredSvc.Spec
		return resultNil, r.Client.Update(ctx, existingSvc)
	}

	reportWorkspaceStorageAvailable(workspaceStorage, existingSvc)
	return resultNil, nil
}
