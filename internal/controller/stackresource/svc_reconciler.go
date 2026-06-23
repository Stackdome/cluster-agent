package stackresource

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
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
	"stackdome.io/cluster-agent/pkg/ingresstls"
)

type svcReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func ResourceSVCName(resource *v1alpha1.StackResource) string {
	return resource.Name
}

func (r *svcReconciler) reconcile(ctx context.Context, resource *v1alpha1.StackResource) (subReconcilerResult, error) {
	if resource.Spec.WorkloadType == v1alpha1.WorkloadTypeWorker {
		reportStackResourceReady(resource)
		return resultNil, nil
	}

	svc, err := r.ensureService(ctx, resource)
	if err != nil {
		return resultNil, err
	}
	if svc == nil {
		return r.serviceNotReady(ctx, resource, "Service for stack resource not created")
	}

	resource.Status.InternalAddress = &svc.Name

	if !resource.Spec.HasExposedPort() {
		reportStackResourceReady(resource)
		return resultNil, nil
	}

	portFqdnMap, err := r.reconcileIngress(ctx, resource, svc)
	if err != nil {
		return resultNil, err
	}
	if portFqdnMap == nil {
		return r.serviceNotReady(ctx, resource, "Ingress for stack resource not created")
	}

	resource.Status.ExternalAddress = buildExternalAddresses(portFqdnMap)
	if resource.Spec.HasExposedPort() {
		setResourceCondition(resource, v1alpha1.StackResourceIngressReady, true, "IngressConfigured", "ingress routes configured for public ports")
	}
	reportStackResourceReady(resource)
	return resultNil, nil
}

func (r *svcReconciler) serviceNotReady(ctx context.Context, resource *v1alpha1.StackResource, message string) (subReconcilerResult, error) {
	controller.LoggerFromContext(ctx).Info("workload svc not ready")
	if resource.Spec.HasExposedPort() {
		setResourceCondition(resource, v1alpha1.StackResourceIngressReady, false, "IngressNotReady", message)
	}
	reportStackResourceNotReady(resource, "ServiceNotReady", message)
	return resultRequeue, nil
}

func buildExternalAddresses(portFqdnMap map[int]string) []v1alpha1.ExternalAddress {
	addresses := make([]v1alpha1.ExternalAddress, 0, len(portFqdnMap))
	for port, fqdn := range portFqdnMap {
		addresses = append(addresses, v1alpha1.ExternalAddress{
			TargetPort: int32(port),
			Address:    fqdn,
		})
	}
	return addresses
}

// --- Ingress reconciliation ---

func (r *svcReconciler) reconcileIngress(
	ctx context.Context,
	resource *v1alpha1.StackResource,
	svc *corev1.Service,
) (map[int]string, error) {
	logger := controller.LoggerFromContext(ctx)
	portFqdnMap, tlsHosts := collectExposedPorts(resource)

	var annotations map[string]string
	var tls []networkingv1.IngressTLS

	if len(tlsHosts) > 0 {
		issuerName, ok, reason, message := ingresstls.ResolveClusterIssuer(ctx, r.Client, logger, resource.Annotations)
		if !ok {
			setTLSCondition(resource, v1.ConditionFalse, reason, message)
			annotations = map[string]string{}
		} else {
			annotations, tls = ingresstls.BuildTLSConfig(issuerName, tlsHosts, resource.Namespace, fmt.Sprintf("%s-tls", resource.Name))
		}
	} else {
		annotations = map[string]string{}
	}

	rules := buildIngressRules(portFqdnMap, svc.Name)

	desired := &networkingv1.Ingress{
		ObjectMeta: v1.ObjectMeta{
			Name:        httpProxyNameForResource(resource.Name),
			Namespace:   resource.Namespace,
			Annotations: annotations,
		},
		Spec: networkingv1.IngressSpec{
			TLS:   tls,
			Rules: rules,
		},
	}
	if err := controllerutil.SetControllerReference(resource, desired, r.Scheme); err != nil {
		return nil, err
	}

	existing := &networkingv1.Ingress{}
	if err := r.Client.Get(ctx, controller.GetNamespacedName(desired), existing); err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.Client.Create(ctx, desired); err != nil {
				return nil, err
			}
			if len(tls) > 0 {
				if err := ingresstls.EnsureRedirectMiddleware(ctx, r.Client, resource.Namespace); err != nil {
					return nil, err
				}
			}
			r.setTLSConfiguredIfEnabled(resource, annotations)
			return nil, nil
		}
		return nil, err
	}

	if ingresstls.IngressNeedsUpdate(desired, existing) {
		existing.Spec = desired.Spec
		ingresstls.SyncManagedAnnotations(existing, annotations)
		if err := r.Client.Update(ctx, existing); err != nil {
			return nil, err
		}
	}

	if len(tls) > 0 {
		if err := ingresstls.EnsureRedirectMiddleware(ctx, r.Client, resource.Namespace); err != nil {
			return nil, err
		}
	}
	r.setTLSConfiguredIfEnabled(resource, annotations)
	return portFqdnMap, nil
}

func (r *svcReconciler) setTLSConfiguredIfEnabled(resource *v1alpha1.StackResource, annotations map[string]string) {
	issuerName := annotations[ingresstls.CertManagerClusterIssuerAnnotation]
	if issuerName != "" {
		setTLSCondition(resource, v1.ConditionTrue, "TLSConfigured",
			fmt.Sprintf("Using ClusterIssuer %q", issuerName))
	}
}

// --- Service reconciliation ---

func (r *svcReconciler) ensureService(ctx context.Context, resource *v1alpha1.StackResource) (*corev1.Service, error) {
	logger := controller.LoggerFromContext(ctx)
	desired := r.buildDesiredService(resource)

	if err := controllerutil.SetControllerReference(resource, &desired, r.Scheme); err != nil {
		return nil, err
	}

	existing := &corev1.Service{}
	key := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	if err := r.Client.Get(ctx, key, existing); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("creating service", "name", desired.Name)
			return nil, r.Client.Create(ctx, &desired)
		}
		return nil, err
	}

	if controller.AreServicesEqual(&desired, existing) {
		return existing, nil
	}

	logger.Info("updating service", "name", desired.Name)
	desired.ResourceVersion = existing.ResourceVersion
	return nil, r.Client.Update(ctx, &desired)
}

func (r *svcReconciler) buildDesiredService(resource *v1alpha1.StackResource) corev1.Service {
	svc := corev1.Service{
		ObjectMeta: v1.ObjectMeta{
			Name:      ResourceSVCName(resource),
			Namespace: resource.Namespace,
			Labels:    identityLabels(resource),
		},
		Spec: corev1.ServiceSpec{
			Selector: GetDeploymentLabelForResource(resource),
		},
	}

	ports := make([]corev1.ServicePort, 0, len(resource.Spec.Ports))
	for _, p := range resource.Spec.Ports {
		ports = append(ports, corev1.ServicePort{
			Name:       p.Name,
			Port:       p.Number,
			TargetPort: intstr.FromString(p.Name),
		})
	}

	if len(ports) > 0 {
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Ports = ports
	} else {
		svc.Spec.ClusterIP = "None"
	}

	return svc
}

func collectExposedPorts(resource *v1alpha1.StackResource) (portFqdnMap map[int]string, tlsHosts []string) {
	portFqdnMap = make(map[int]string)
	for _, port := range resource.Spec.Ports {
		if !port.ExposeToPublic {
			continue
		}
		portFqdnMap[int(port.Number)] = port.FQDN
		if port.TLS {
			tlsHosts = append(tlsHosts, port.FQDN)
		}
	}
	return
}

func setTLSCondition(resource *v1alpha1.StackResource, status v1.ConditionStatus, reason, message string) {
	ingresstls.SetTLSCondition(&resource.Status.Conditions, resource.Generation, string(v1alpha1.StackResourceTLSConfigured), status, reason, message)
}

func buildIngressRules(portFqdnMap map[int]string, serviceName string) []networkingv1.IngressRule {
	rules := make([]networkingv1.IngressRule, 0, len(portFqdnMap))
	for port, fqdn := range portFqdnMap {
		rules = append(rules, networkingv1.IngressRule{
			Host: fqdn,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Path:     "/",
						PathType: ptr.To(networkingv1.PathTypePrefix),
						Backend: networkingv1.IngressBackend{
							Service: &networkingv1.IngressServiceBackend{
								Name: serviceName,
								Port: networkingv1.ServiceBackendPort{Number: int32(port)},
							},
						},
					}},
				},
			},
		})
	}
	return rules
}

func httpProxyNameForResource(resourceName string) string {
	return fmt.Sprintf("%s-http-proxy", resourceName)
}
