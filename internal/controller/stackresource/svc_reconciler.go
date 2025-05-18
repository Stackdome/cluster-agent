package stackresource

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

type svcReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func ResourceSVCName(resource *v1alpha1.StackResource) string {
	return resource.Name
}

func (r *svcReconciler) reconcile(ctx context.Context, resource *v1alpha1.StackResource) (subReconcilerResult, error) {
	svc, err := r.ensureSvc(ctx, resource, resource.Spec.Ports)
	if err != nil {
		return resultNil, err
	}

	if svc == nil {
		return r.handleServiceNotReady(ctx)
	}

	resource.Status.InternalAddress = &svc.Name
	if !resource.Spec.HasExposedPort() {
		reportStackResourceReady(resource)
		return resultNil, nil
	}

	portFqdnMap, err := r.reconcileIngressForService(ctx, resource, svc)
	if err != nil {
		return resultNil, err
	}

	if portFqdnMap == nil {
		return r.handleServiceNotReady(ctx)
	}
	resource.Status.ExternalAddress = r.buildExternalAddresses(portFqdnMap)
	reportStackResourceReady(resource)
	return resultNil, nil
}

func (r *svcReconciler) handleServiceNotReady(ctx context.Context) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("workload svc not ready")
	return resultRequeue, nil
}

func (r *svcReconciler) buildExternalAddresses(portFqdnMap map[int]string) []v1alpha1.ExternalAddress {
	externalAddresses := make([]v1alpha1.ExternalAddress, 0, len(portFqdnMap))
	for externalPort, fqdn := range portFqdnMap {
		externalAddresses = append(externalAddresses, v1alpha1.ExternalAddress{
			TargetPort: int32(externalPort),
			Address:    fqdn,
		})
	}
	return externalAddresses
}

func (r *svcReconciler) reconcileIngressForService(
	ctx context.Context,
	resource *v1alpha1.StackResource,
	serviceToBeExposed *corev1.Service) (map[int]string, error) {
	exposedPortFqdnMap := map[int]string{}
	for _, port := range resource.Spec.Ports {
		if port.ExposeToPublic {
			// RHS should be unique across all users.
			exposedPortFqdnMap[int(port.Number)] = port.FQDN
		}
	}

	rules := []networkingv1.IngressRule{}
	for exposedPort, fqdn := range exposedPortFqdnMap {
		rules = append(rules, networkingv1.IngressRule{
			Host: fqdn,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{
						{
							Path:     "/",
							PathType: ptr.To(networkingv1.PathTypePrefix),
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: serviceToBeExposed.Name,
									Port: networkingv1.ServiceBackendPort{
										Number: int32(exposedPort),
									},
								},
							},
						},
					},
				},
			},
		})
	}
	desiredIngress := &networkingv1.Ingress{
		ObjectMeta: v1.ObjectMeta{
			Name:        httpProxyNameForResource(resource.Name),
			Namespace:   resource.Namespace,
			Annotations: map[string]string{},
		},
		Spec: networkingv1.IngressSpec{
			Rules: rules,
		},
	}

	if err := controllerutil.SetControllerReference(resource, desiredIngress, r.Scheme); err != nil {
		return nil, err
	}

	existingIngress := &networkingv1.Ingress{}

	if err := r.Client.Get(ctx, controller.GetNamespacedName(desiredIngress), existingIngress); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, r.Client.Create(ctx, desiredIngress)
		}
		return nil, err
	}

	if !equality.Semantic.DeepDerivative(desiredIngress.Spec, existingIngress.Spec) {
		existingIngress.Spec = desiredIngress.Spec
		return nil, r.Client.Update(ctx, existingIngress)
	}
	return exposedPortFqdnMap, nil
}

func httpProxyNameForResource(resourceName string) string {
	return fmt.Sprintf("%s-http-proxy", resourceName)
}

func (r *svcReconciler) ensureSvc(ctx context.Context, resource *v1alpha1.StackResource, ports []v1alpha1.Port) (*corev1.Service, error) {
	logger := controller.LoggerFromContext(ctx)
	svcPorts := make([]corev1.ServicePort, 0)
	for _, port := range ports {
		svcPorts = append(svcPorts, corev1.ServicePort{
			Port:       port.Number,
			TargetPort: intstr.FromInt(int(port.Number)),
		})
	}

	var desiredSvc corev1.Service
	if len(svcPorts) > 0 {
		desiredSvc = corev1.Service{
			ObjectMeta: v1.ObjectMeta{
				Name:      ResourceSVCName(resource),
				Namespace: resource.Namespace,
			},
			Spec: corev1.ServiceSpec{
				Selector: GetDeploymentPodLabelForResource(resource),
				Type:     corev1.ServiceTypeClusterIP,
				Ports:    svcPorts,
			},
		}
	} else {
		desiredSvc = corev1.Service{
			ObjectMeta: v1.ObjectMeta{
				Name:      ResourceSVCName(resource),
				Namespace: resource.Namespace,
			},
			Spec: corev1.ServiceSpec{
				Selector:  GetDeploymentPodLabelForResource(resource),
				ClusterIP: "None",
			},
		}
	}

	if err := controllerutil.SetControllerReference(resource, &desiredSvc, r.Scheme); err != nil {
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
			return nil, r.Client.Create(ctx, &desiredSvc)
		}
		return nil, err
	}
	if controller.AreServicesEqual(&desiredSvc, existingSvc) {
		return existingSvc, nil
	}
	desiredSvc.ResourceVersion = existingSvc.ResourceVersion
	return nil, r.Client.Update(ctx, &desiredSvc)
}
