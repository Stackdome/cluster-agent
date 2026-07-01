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
	"stackdome.io/cluster-agent/internal/controller/stackresource/workload"
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
	// Worker, Job, and CronJob workloads don't need a Service or Ingress.
	switch resource.Spec.WorkloadType {
	case v1alpha1.WorkloadTypeWorker, v1alpha1.WorkloadTypeJob, v1alpha1.WorkloadTypeCronJob:
		reportStackResourceReady(resource)
		return resultNil, nil
	}

	svc, err := r.ensureService(ctx, resource)
	if err != nil {
		return resultNil, err
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

	// The resource has an exposed port (checked above) and the Ingress is now reconciled.
	// An Ingress has no async readiness, so publish the public routes in this same pass.
	resource.Status.ExternalAddress = buildExternalAddresses(resource, portFqdnMap)
	setResourceCondition(resource, v1alpha1.StackResourceIngressReady, true, "IngressConfigured", "ingress routes configured for public ports")
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

func buildExternalAddresses(resource *v1alpha1.StackResource, portFqdnMap map[int]string) []v1alpha1.ExternalAddress {
	tlsPorts := make(map[int32]bool, len(resource.Spec.Ports))
	for _, p := range resource.Spec.Ports {
		if p.TLS {
			tlsPorts[p.Number] = true
		}
	}
	addresses := make([]v1alpha1.ExternalAddress, 0, len(portFqdnMap))
	for port, fqdn := range portFqdnMap {
		scheme := "http://"
		if tlsPorts[int32(port)] {
			scheme = "https://"
		}
		addresses = append(addresses, v1alpha1.ExternalAddress{
			TargetPort: int32(port),
			Address:    scheme + fqdn,
		})
	}
	return addresses
}

// --- Ingress reconciliation ---

// reconcileIngress ensures the Ingress for a resource's exposed ports exists and is up to
// date, and returns the port→FQDN map so reconcile() can publish external addresses. An
// Ingress is a declarative object with no async readiness of its own, and the FQDNs come
// from the spec (not the Ingress status), so there is nothing to wait for after creating
// it — addresses are published in the same reconcile pass.
func (r *svcReconciler) reconcileIngress(
	ctx context.Context,
	resource *v1alpha1.StackResource,
	svc *corev1.Service,
) (map[int]string, error) {
	portFqdnMap, tlsHosts := collectExposedPorts(resource)
	annotations, tls := r.resolveIngressTLS(ctx, resource, tlsHosts)

	desired := buildDesiredIngress(resource, buildIngressRules(portFqdnMap, svc.Name), annotations, tls)
	if err := controllerutil.SetControllerReference(resource, desired, r.Scheme); err != nil {
		return nil, err
	}

	if err := r.applyIngress(ctx, desired, annotations); err != nil {
		return nil, err
	}

	if len(tls) > 0 {
		if err := ingresstls.EnsureRedirectMiddleware(ctx, r.Client, resource.Namespace); err != nil {
			return nil, err
		}
	}
	r.setTLSConfiguredIfEnabled(resource, annotations)

	return portFqdnMap, nil
}

// resolveIngressTLS returns the cert-manager/Traefik annotations and TLS blocks for the
// Ingress. It returns empty annotations and no TLS when no port requests TLS, or when a
// port requests TLS but the ClusterIssuer cannot be resolved (recording the reason on the
// resource's TLS condition).
func (r *svcReconciler) resolveIngressTLS(
	ctx context.Context,
	resource *v1alpha1.StackResource,
	tlsHosts []string,
) (map[string]string, []networkingv1.IngressTLS) {
	if len(tlsHosts) == 0 {
		return map[string]string{}, nil
	}
	logger := controller.LoggerFromContext(ctx)
	issuerName, ok, reason, message := ingresstls.ResolveClusterIssuer(ctx, r.Client, logger, resource.Annotations)
	if !ok {
		setTLSCondition(resource, v1.ConditionFalse, reason, message)
		return map[string]string{}, nil
	}
	return ingresstls.BuildTLSConfig(issuerName, tlsHosts, resource.Namespace, fmt.Sprintf("%s-tls", resource.Name))
}

// buildDesiredIngress constructs the desired Ingress object. It is pure: all cluster
// lookups (TLS issuer resolution) happen in resolveIngressTLS before this is called.
func buildDesiredIngress(
	resource *v1alpha1.StackResource,
	rules []networkingv1.IngressRule,
	annotations map[string]string,
	tls []networkingv1.IngressTLS,
) *networkingv1.Ingress {
	return &networkingv1.Ingress{
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
}

// applyIngress creates the Ingress when absent or updates it in place when its managed
// spec/annotations have drifted.
func (r *svcReconciler) applyIngress(
	ctx context.Context,
	desired *networkingv1.Ingress,
	annotations map[string]string,
) error {
	existing := &networkingv1.Ingress{}
	if getErr := r.Client.Get(ctx, controller.GetNamespacedName(desired), existing); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			return r.Client.Create(ctx, desired)
		}
		return getErr
	}

	if ingresstls.IngressNeedsUpdate(desired, existing) {
		existing.Spec = desired.Spec
		ingresstls.SyncManagedAnnotations(existing, annotations)
		return r.Client.Update(ctx, existing)
	}
	return nil
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
			return &desired, r.Client.Create(ctx, &desired)
		}
		return nil, err
	}

	if controller.AreServicesEqual(&desired, existing) {
		return existing, nil
	}

	logger.Info("updating service", "name", desired.Name)
	desired.ResourceVersion = existing.ResourceVersion
	return &desired, r.Client.Update(ctx, &desired)
}

// buildDesiredService builds the Service for a workload.
//
// A StatefulService always gets a headless Service (ClusterIP=None): the same object
// serves as the StatefulSet's governing Service (stable per-pod DNS) and, when a port is
// exposed, as the Ingress backend. Ingress is still created for a StatefulService (the
// skip list in reconcile only covers Worker/Job/CronJob) — routing to a headless Service
// works with ingress controllers that resolve the Service's Endpoints/pod IPs rather than
// a ClusterIP. A portless workload is also headless (nothing to load-balance); every other
// workload gets a normal ClusterIP.
func (r *svcReconciler) buildDesiredService(resource *v1alpha1.StackResource) corev1.Service {
	ports := buildServicePorts(resource)

	svc := corev1.Service{
		ObjectMeta: v1.ObjectMeta{
			Name:      ResourceSVCName(resource),
			Namespace: resource.Namespace,
			Labels:    workload.IdentityLabels(resource),
		},
		Spec: corev1.ServiceSpec{
			Selector: workload.GetWorkloadLabelForResource(resource),
		},
	}
	// Leave Ports unset (nil) rather than an empty slice when there are none, so the
	// desired object matches a server-defaulted headless Service and AreServicesEqual
	// does not see spurious drift.
	if len(ports) > 0 {
		svc.Spec.Ports = ports
	}

	if isHeadlessService(resource, ports) {
		svc.Spec.ClusterIP = "None"
	} else {
		svc.Spec.Type = corev1.ServiceTypeClusterIP
	}

	return svc
}

// isHeadlessService reports whether the workload's Service should be headless: always for
// a StatefulService, and for any workload with no ports (nothing to load-balance).
func isHeadlessService(resource *v1alpha1.StackResource, ports []corev1.ServicePort) bool {
	return resource.Spec.WorkloadType == v1alpha1.WorkloadTypeStatefulService || len(ports) == 0
}

func buildServicePorts(resource *v1alpha1.StackResource) []corev1.ServicePort {
	ports := make([]corev1.ServicePort, 0, len(resource.Spec.Ports))
	for _, p := range resource.Spec.Ports {
		ports = append(ports, corev1.ServicePort{
			Name:       p.Name,
			Port:       p.Number,
			TargetPort: intstr.FromString(p.Name),
		})
	}
	return ports
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
