package workspaceresource

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

type svcReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	w      *WorkspaceResourceReconciler
}

func ResourceSVCName(resource *v1alpha1.WorkspaceResource) string {
	return fmt.Sprintf("%s-svc", resource.Name)
}

func (r *svcReconciler) reconcile(ctx context.Context, resource *v1alpha1.WorkspaceResource) (subReconcilerResult, error) {
	internalPorts, externalPorts := resource.SplitPortsByInternalAndExternal()
	internalServiceReady := true
	externalServiceReady := true

	if len(internalPorts) > 0 {
		internalSvc, err := r.ensureSvc(ctx, resource, internalPorts, false)
		if err != nil {
			return resultNil, err
		}
		if internalSvc != nil {
			internalServiceReady = true
			resource.Status.InternalAddress = internalSvc.Name
		} else {
			internalServiceReady = false
		}
	}

	if len(externalPorts) > 0 {
		externalSvc, err := r.ensureSvc(ctx, resource, externalPorts, true)
		if err != nil {
			return resultNil, err
		}
		if externalSvc != nil {
			nodeIP, err := controller.GetNodeIP(ctx, r.Client)
			if err != nil {
				return resultNil, err
			}
			externalServiceReady = true
			resource.Status.ExternalAddress = nodeIP
		} else {
			externalServiceReady = false
		}
	}
	// IF either of the services are not, we arrange for a requeue, the service was just created maybe.
	if internalServiceReady && externalServiceReady {
		r.w.reportWorkspaceResourceReady(resource)
		return resultNil, nil
	}
	logger := controller.LoggerFromContext(ctx)
	logger.Info("either of the workload svc not ready")
	return resultRequeue, nil
}

func (r *svcReconciler) ensureSvc(ctx context.Context, resource *v1alpha1.WorkspaceResource, ports []v1alpha1.Port, exposed bool) (*corev1.Service, error) {
	logger := controller.LoggerFromContext(ctx)
	if len(ports) == 0 {
		return nil, nil
	}
	var serviceType corev1.ServiceType
	if exposed {
		serviceType = corev1.ServiceTypeNodePort
	} else {
		serviceType = corev1.ServiceTypeClusterIP
	}

	svcPorts := make([]corev1.ServicePort, 0)
	for _, port := range ports {
		svcPorts = append(svcPorts, corev1.ServicePort{
			Protocol:   "TCP",
			Port:       port.Number,
			TargetPort: intstr.FromInt(int(port.Number)),
		})
	}

	desiredSvc := &corev1.Service{
		ObjectMeta: v1.ObjectMeta{
			Name:      ResourceSVCName(resource),
			Namespace: resource.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: GetDeploymentPodLabelForResource(resource),
			Type:     serviceType,
			Ports:    svcPorts,
		},
	}
	if err := controllerutil.SetControllerReference(resource, desiredSvc, r.Scheme); err != nil {
		return nil, err
	}
	logger.Info("in ensure svc")
	existingSvc := &corev1.Service{}
	if err := r.Client.Get(
		ctx,
		types.NamespacedName{
			Name:      desiredSvc.Name,
			Namespace: desiredSvc.Namespace,
		},
		existingSvc,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, r.Client.Create(ctx, desiredSvc)
		}
		return nil, err
	}
	if controller.AreServicesEqual(desiredSvc, existingSvc) {
		return existingSvc, nil
	}
	logger.Info(cmp.Diff(desiredSvc.Spec, existingSvc.Spec))
	return nil, nil
}
