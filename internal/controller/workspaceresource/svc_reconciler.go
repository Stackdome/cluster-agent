package workspaceresource

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
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
	return resource.Name
}

func (r *svcReconciler) reconcile(ctx context.Context, resource *v1alpha1.WorkspaceResource) (subReconcilerResult, error) {
	workspace, err := r.getWorkspace(ctx, resource)
	if err != nil {
		return resultNil, err
	}
	svc, err := r.ensureSvc(ctx, resource, resource.Spec.Ports)
	if err != nil {
		return resultNil, err
	}

	if svc == nil {
		return r.handleServiceNotReady(ctx)
	}

	resource.Status.InternalAddress = &svc.Name
	if !resource.Spec.HasExposedPort() {
		r.w.reportWorkspaceResourceReady(resource)
		return resultNil, nil
	}

	portSubdomainMap, err := r.reconcileHttpProxyForService(ctx, resource, svc, workspace)
	if err != nil {
		return resultNil, err
	}

	if portSubdomainMap == nil {
		return r.handleServiceNotReady(ctx)
	}
	resource.Status.ExternalAddress = r.buildExternalAddresses(portSubdomainMap, workspace.Spec.Domain)
	r.w.reportWorkspaceResourceReady(resource)
	return resultNil, nil
}

func (r *svcReconciler) handleServiceNotReady(ctx context.Context) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("workload svc not ready")
	return resultRequeue, nil
}

func (r *svcReconciler) buildExternalAddresses(portSubdomainMap map[int]string, domain string) []v1alpha1.ExternalAddress {
	externalAddresses := make([]v1alpha1.ExternalAddress, 0, len(portSubdomainMap))
	for externalPort, subdomainForExposedPort := range portSubdomainMap {
		externalAddresses = append(externalAddresses, v1alpha1.ExternalAddress{
			TargetPort: int32(externalPort),
			Address:    fmt.Sprintf("%s.%s", subdomainForExposedPort, domain),
		})
	}
	return externalAddresses
}

func (r *svcReconciler) reconcileHttpProxyForService(
	ctx context.Context,
	resource *v1alpha1.WorkspaceResource,
	serviceToBeExposed *corev1.Service,
	workspace *v1alpha1.Workspace) (map[int]string, error) {
	exposedPortSubdomainMap := map[int]string{}
	for _, port := range resource.Spec.Ports {
		if port.ExposeToPublic {
			// RHS should be unique across all users.
			exposedPortSubdomainMap[int(port.Number)] = port.Subdomain
		}
	}

	rules := []networkingv1.IngressRule{}
	for exposedPort := range exposedPortSubdomainMap {
		rules = append(rules, networkingv1.IngressRule{
			Host: fmt.Sprintf("%s.%s", exposedPortSubdomainMap[exposedPort], workspace.Spec.Domain),
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
			Name:      httpProxyNameForResource(resource.Name),
			Namespace: resource.Namespace,
			Annotations: map[string]string{
				"projectcontour.io/websocket-routes": "/",
				"projectcontour.io/response-timeout": "3600s",
			},
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
	return exposedPortSubdomainMap, nil
}

func httpProxyNameForResource(resourceName string) string {
	return fmt.Sprintf("%s-http-proxy", resourceName)
}

func (r *svcReconciler) ensureSvc(ctx context.Context, resource *v1alpha1.WorkspaceResource, ports []v1alpha1.Port) (*corev1.Service, error) {
	logger := controller.LoggerFromContext(ctx)
	svcPorts := make([]corev1.ServicePort, 0)
	for _, port := range ports {
		svcPorts = append(svcPorts, corev1.ServicePort{
			Protocol:   "TCP",
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
	return nil, nil
}

func (r *svcReconciler) getWorkspace(ctx context.Context, workpaceResource *v1alpha1.WorkspaceResource) (*v1alpha1.Workspace, error) {
	workspaceRef := metav1.GetControllerOf(workpaceResource)
	if workspaceRef == nil {
		return nil, fmt.Errorf("missing owner ref for resource")
	}
	workspace := &v1alpha1.Workspace{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: workspaceRef.Name, Namespace: workpaceResource.Namespace}, workspace); err != nil {
		return nil, err
	}
	return workspace, nil
}
