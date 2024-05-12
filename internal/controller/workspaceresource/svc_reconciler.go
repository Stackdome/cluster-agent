package workspaceresource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

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
	return fmt.Sprintf("%s-svc", resource.Name)
}

func (r *svcReconciler) reconcile(ctx context.Context, resource *v1alpha1.WorkspaceResource) (subReconcilerResult, error) {
	if len(resource.Spec.Ports) > 0 {
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

		portSubdomainMap, err := r.reconcileHttpProxyForService(ctx, resource, svc)
		if err != nil {
			return resultNil, err
		}

		if portSubdomainMap == nil {
			return r.handleServiceNotReady(ctx)
		}

		resource.Status.ExternalAddress = r.buildExternalAddresses(portSubdomainMap)
	}
	r.w.reportWorkspaceResourceReady(resource)
	return resultNil, nil
}

func (r *svcReconciler) handleServiceNotReady(ctx context.Context) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("workload svc not ready")
	return resultRequeue, nil
}

func (r *svcReconciler) buildExternalAddresses(portSubdomainMap map[int]string) []v1alpha1.ExternalAddress {
	externalAddresses := make([]v1alpha1.ExternalAddress, 0, len(portSubdomainMap))
	for externalPort, subdomainForExposedPort := range portSubdomainMap {
		externalAddresses = append(externalAddresses, v1alpha1.ExternalAddress{
			TargetPort: int32(externalPort),
			Address:    fmt.Sprintf("%s.voyager.test", subdomainForExposedPort),
		})
	}
	return externalAddresses
}

func (r *svcReconciler) reconcileHttpProxyForService(ctx context.Context, resource *v1alpha1.WorkspaceResource, serviceToBeExposed *corev1.Service) (map[int]string, error) {
	workspaceRef := metav1.GetControllerOf(resource)
	if workspaceRef == nil {
		return nil, fmt.Errorf("missing owner ref for resource")
	}

	workspace := &v1alpha1.Workspace{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: workspaceRef.Name, Namespace: resource.Namespace}, workspace); err != nil {
		return nil, err
	}
	exposedPortSubdomainMap := map[int]string{}
	for i, port := range resource.Spec.Ports {
		if port.ExposeToPublic {
			// RHS should be unique across all users.
			exposedPortSubdomainMap[int(port.Number)] = createShortHash(workspace.Name, workspace.Namespace, resource.Name, intstr.FromInt(i).StrVal)
		}
	}

	rules := []networkingv1.IngressRule{}
	for exposedPort := range exposedPortSubdomainMap {
		rules = append(rules, networkingv1.IngressRule{
			Host: fmt.Sprintf("%s.voyager.test", exposedPortSubdomainMap[exposedPort]),
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
	if len(ports) == 0 {
		return nil, nil
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
			Type:     corev1.ServiceTypeClusterIP,
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
	return nil, nil
}

func findServicePort(inputPort v1alpha1.Port, svc *corev1.Service) int32 {
	for _, port := range svc.Spec.Ports {
		if inputPort.Number == port.TargetPort.IntVal {
			return port.NodePort
		}
	}
	// TODO: Handle this case!!
	return -1
}

func createShortHash(inputStrings ...string) string {
	concatenated := strings.Join(inputStrings, ",")
	hash := sha256.Sum256([]byte(concatenated))
	shortHash := hex.EncodeToString(hash[:6])
	return strings.ToLower(shortHash)
}
