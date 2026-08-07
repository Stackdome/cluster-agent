package stackresource

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
		return resultNil, nil
	}

	// Reject invalid route layouts before writing the Service, so a bad TLS ref cannot leave
	// partial resource state behind. reconcileIngress collects the routes again to execute it.
	if _, err := collectExposedPortRoutes(resource); err != nil {
		return resultNil, err
	}

	svc, err := r.ensureService(ctx, resource)
	if err != nil {
		return resultNil, err
	}

	resource.Status.InternalAddress = &svc.Name

	if !resource.Spec.HasExposedPort() {
		if err := r.cleanupIngress(ctx, resource); err != nil {
			return resultNil, err
		}
		resource.Status.ExternalAddress = nil
		return resultNil, nil
	}

	state, err := r.reconcileIngress(ctx, resource, svc)
	if err != nil {
		return resultNil, err
	}
	// The resource has an exposed port (checked above) and the Ingress is now reconciled.
	// An Ingress has no async readiness, so publish the public routes in this same pass.
	resource.Status.ExternalAddress = buildExternalAddresses(
		resource,
		state.publicFQDNs,
		state.certManagerTLS.Stage,
		state.referencedTLS.Stage,
	)
	setResourceCondition(resource, v1alpha1.StackResourceIngressReady, true, "IngressConfigured", "ingress routes configured for public ports")

	// TLS object watches wake us when a certificate becomes ready. A stalled issuance has
	// no event at the grace deadline, so schedule that pass. A deferred requeue does not
	// stop the rest of the resource reconciliation.
	if retryAfter := earliestRetry(state.certManagerTLS, state.referencedTLS); retryAfter > 0 {
		return resultDeferredRequeue(retryAfter), nil
	}
	return resultNil, nil
}

// buildExternalAddresses publishes the public addresses for exposed ports. A port without
// TLS publishes http:// immediately. A port with TLS publishes nothing while the certificate
// is still being issued — handing a user an https:// URL that serves Traefik's self-signed
// default is what poisons their browser's security state for the origin. Once issuance
// terminally fails or times out the port falls back to http://, so the site stays reachable
// rather than being permanently unlinkable. A port pointing at a referenced TLS Secret
// waits on its own signal instead of the issuance stage: the replica of that secret being
// in the workload namespace, which is the only thing that makes the host serve real HTTPS.
func buildExternalAddresses(
	resource *v1alpha1.StackResource,
	portFQDNs map[int]string,
	certManagerTLSStage tlsStage,
	referencedTLSStage tlsStage,
) []v1alpha1.ExternalAddress {
	addresses := make([]v1alpha1.ExternalAddress, 0, len(portFQDNs))
	for port, fqdn := range portFQDNs {
		address := buildExternalAddress(
			int32(port),
			fqdn,
			resource,
			certManagerTLSStage,
			referencedTLSStage,
		)
		if address != nil {
			addresses = append(addresses, *address)
		}
	}
	return addresses
}

func buildExternalAddress(
	port int32,
	fqdn string,
	resource *v1alpha1.StackResource,
	certManagerTLSStage tlsStage,
	referencedTLSStage tlsStage,
) *v1alpha1.ExternalAddress {
	portSpec := resource.GetPort(port)
	if portSpec == nil || !portSpec.ExposeToPublic {
		return nil
	}

	if !portSpec.TLS {
		return &v1alpha1.ExternalAddress{
			TargetPort: port,
			Address:    "http://" + fqdn,
		}
	}

	tlsStage := certManagerTLSStage
	if _, usesReferencedTLS := portReferencedTLSSecretRef(*portSpec); usesReferencedTLS {
		tlsStage = referencedTLSStage
	}

	switch tlsStage {
	case tlsStageReady:
		return &v1alpha1.ExternalAddress{
			TargetPort: port,
			Address:    "https://" + fqdn,
		}
	case tlsStageUnavailable:
		return &v1alpha1.ExternalAddress{
			TargetPort: port,
			Address:    "http://" + fqdn,
		}
	default:
		return nil
	}
}

// --- Ingress reconciliation ---

func (r *svcReconciler) cleanupIngress(ctx context.Context, resource *v1alpha1.StackResource) error {
	for _, name := range []string{
		httpProxyNameForResource(resource.Name),
		referencedTLSIngressNameForResource(resource.Name),
	} {
		if err := r.deleteChildIngress(ctx, resource, name); err != nil {
			return fmt.Errorf("delete Ingress %s: %w", name, err)
		}
	}

	resource.Status.ReferencedTLSSecret = nil
	resource.Status.CertManagerTLS = nil
	meta.RemoveStatusCondition(&resource.Status.Conditions, string(v1alpha1.StackResourceTLSConfigured))
	meta.RemoveStatusCondition(&resource.Status.Conditions, string(v1alpha1.StackResourceIngressReady))
	return nil
}

// ingressState is what reconcile() needs from reconcileIngress in order to publish the
// resource's external addresses.
type ingressState struct {
	publicFQDNs    map[int]string
	certManagerTLS tlsState
	referencedTLS  tlsState
}

// reconcileIngress ensures the Ingresses for a resource's exposed ports exist and are up to
// date, and returns the port→FQDN map so reconcile() can publish external addresses. An
// Ingress is a declarative object with no async readiness of its own, and the FQDNs come
// from the spec (not the Ingress status), so there is nothing to wait for after creating
// it — addresses are published in the same reconcile pass.
// Ingress = rules + TLS + Annotations
func (r *svcReconciler) reconcileIngress(
	ctx context.Context,
	resource *v1alpha1.StackResource,
	svc *corev1.Service,
) (ingressState, error) {
	routes, err := collectExposedPortRoutes(resource)
	if err != nil {
		return ingressState{}, err
	}

	certManagerStatus, err := r.reconcileCertManagerIngress(ctx, resource, svc, routes)
	if err != nil {
		return ingressState{}, err
	}

	referencedTLSStatus, err := r.reconcileReferencedTLSIngress(ctx, resource, svc, routes)
	if err != nil {
		return ingressState{}, err
	}

	if certManagerStatus.Stage == tlsStageReady || referencedTLSStatus.Stage == tlsStageReady {
		if err := ingresstls.EnsureRedirectMiddleware(ctx, r.Client, resource.Namespace); err != nil {
			return ingressState{}, err
		}
	}
	setTLSConfigured(resource, certManagerStatus, referencedTLSStatus)

	return ingressState{
		publicFQDNs:    routes.allFQDNs,
		certManagerTLS: certManagerStatus,
		referencedTLS:  referencedTLSStatus,
	}, nil
}

func (r *svcReconciler) reconcileCertManagerIngress(
	ctx context.Context,
	resource *v1alpha1.StackResource,
	svc *corev1.Service,
	routes exposedPortRoutes,
) (tlsState, error) {
	rules := buildIngressRules(routes.certManagerIngressFQDNs, svc.Name)
	if len(routes.certManagerHosts) == 0 {
		status := tlsState{}
		updateCertManagerTLSStatus(resource, status)
		return status, r.reconcileIngressCR(
			ctx, resource, httpProxyNameForResource(resource.Name), rules, map[string]string{}, nil,
		)
	}

	logger := controller.LoggerFromContext(ctx)
	issuerName, found, reason, message := ingresstls.ResolveClusterIssuer(ctx, r.Client, logger, resource.Annotations)
	if !found {
		status := tlsState{
			Stage:   tlsStageUnavailable,
			Reason:  reason,
			Message: message,
		}
		updateCertManagerTLSStatus(resource, tlsState{})
		return status, r.reconcileIngressCR(
			ctx, resource, httpProxyNameForResource(resource.Name), rules, map[string]string{}, nil,
		)
	}

	status, err := r.getCertManagerTLSState(ctx, resource)
	if err != nil {
		return tlsState{}, err
	}
	updateCertManagerTLSStatus(resource, status)
	annotations, tls := ingresstls.BuildTLSConfig(
		issuerName,
		routes.certManagerHosts,
		resource.Namespace,
		certificateNameForResource(resource.Name),
	)
	if status.Stage != tlsStageReady {
		delete(annotations, ingresstls.TraefikMiddlewaresAnnotation)
	}
	err = r.reconcileIngressCR(
		ctx, resource, httpProxyNameForResource(resource.Name), rules, annotations, tls,
	)
	return status, err
}

func (r *svcReconciler) reconcileReferencedTLSIngress(
	ctx context.Context,
	resource *v1alpha1.StackResource,
	svc *corev1.Service,
	routes exposedPortRoutes,
) (tlsState, error) {
	status, err := r.reconcileReferencedTLSSecret(ctx, resource, routes.referencedTLSSecretRef)
	if err != nil {
		return tlsState{}, err
	}

	annotations := map[string]string{}
	if status.Stage == tlsStageReady {
		annotations = ingresstls.TraefikAnnotations(resource.Namespace)
	}
	tls := buildReferencedTLSConfig(routes.referencedTLSSecretRef, routes.referencedTLSHosts, status.Stage)
	err = r.reconcileIngressCR(
		ctx,
		resource,
		referencedTLSIngressNameForResource(resource.Name),
		buildIngressRules(routes.referencedTLSFQDNs, svc.Name),
		annotations,
		tls,
	)
	return status, err
}

func earliestRetry(statuses ...tlsState) time.Duration {
	var earliest time.Duration
	for _, status := range statuses {
		if status.RetryAfter > 0 && (earliest == 0 || status.RetryAfter < earliest) {
			earliest = status.RetryAfter
		}
	}
	return earliest
}

// reconcileIngressCR applies an Ingress when it has rules and deletes it when
// its route bucket is empty.
func (r *svcReconciler) reconcileIngressCR(
	ctx context.Context,
	resource *v1alpha1.StackResource,
	name string,
	rules []networkingv1.IngressRule,
	annotations map[string]string,
	tls []networkingv1.IngressTLS,
) error {
	if len(rules) == 0 {
		return r.deleteChildIngress(ctx, resource, name)
	}

	desired := buildDesiredIngress(resource, name, rules, annotations, tls)
	if err := controllerutil.SetControllerReference(resource, desired, r.Scheme); err != nil {
		return err
	}
	return r.applyIngress(ctx, desired, annotations)
}

// deleteChildIngress removes an Ingress this resource no longer needs. It reads from the
// cache before writing for two reasons:
//   - a by-name Delete is a write call (and an audit entry) on every reconcile of every
//     resource with exposed ports, and most of them never had this Ingress.
//   - a same-named Ingress we are not the controller of belongs to someone else.
func (r *svcReconciler) deleteChildIngress(ctx context.Context, resource *v1alpha1.StackResource, name string) error {
	stale := &networkingv1.Ingress{}
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: resource.Namespace, Name: name}, stale); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !v1.IsControlledBy(stale, resource) {
		controller.LoggerFromContext(ctx).Info("not deleting an Ingress owned by someone else",
			"ingress", controller.GetNamespacedName(stale))
		return nil
	}
	return client.IgnoreNotFound(r.Client.Delete(ctx, stale))
}

// buildDesiredIngress constructs the desired Ingress object. It is pure: all cluster
// lookups happen before this is called.
func buildDesiredIngress(
	resource *v1alpha1.StackResource,
	name string,
	rules []networkingv1.IngressRule,
	annotations map[string]string,
	tls []networkingv1.IngressTLS,
) *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: v1.ObjectMeta{
			Name:        name,
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

// setTLSConfigured writes TLSConfigured from both TLS paths a resource can use: cert-manager
// issuance and replica sync for referenced TLS Secrets.
//   - True only once a certificate is actually usable — not merely when an issuer was found
//     and the annotations were applied.
//   - A resource on both paths is the union: whichever is still waiting wins, so any port
//     without working TLS keeps the condition False.
//   - Neither path in play (no TLS port at all) leaves the condition untouched.
func setTLSConfigured(resource *v1alpha1.StackResource, cert, refs tlsState) {
	for _, status := range []tlsState{refs, cert} {
		if status.Stage != tlsStageNone && status.Stage != tlsStageReady {
			setTLSCondition(resource, v1.ConditionFalse, status.Reason, status.Message)
			return
		}
	}
	switch {
	case cert.Stage == tlsStageReady:
		setTLSCondition(resource, v1.ConditionTrue, cert.Reason, cert.Message)
	case refs.Stage == tlsStageReady:
		setTLSCondition(resource, v1.ConditionTrue, refs.Reason, refs.Message)
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

// exposedPortRoutes groups public ports by the Ingress that will serve them.
type exposedPortRoutes struct {
	// All FQDNs are used for published addresses.
	allFQDNs map[int]string // every public port, used for published addresses

	// Standard Ingress: plain HTTP and cert-manager-managed TLS.
	certManagerIngressFQDNs map[int]string // plain HTTP and cert-manager-managed TLS
	certManagerHosts        []string       // hosts requiring individual certificate issuance

	referencedTLSSecretRef string
	referencedTLSFQDNs     map[int]string // TLS backed by a copied Secret
	referencedTLSHosts     []string
}

// collectExposedPortRoutes splits the public ports. A TLSSecretRef that is not
// "<namespace>/<name>" is ignored, so the port falls back to the cert-manager path
// rather than losing TLS.
func collectExposedPortRoutes(resource *v1alpha1.StackResource) (exposedPortRoutes, error) {
	routes := exposedPortRoutes{
		allFQDNs:                map[int]string{},
		certManagerIngressFQDNs: map[int]string{},
		referencedTLSFQDNs:      map[int]string{},
	}
	for _, port := range resource.Spec.Ports {
		if !port.ExposeToPublic {
			continue
		}
		number := int(port.Number)
		routes.allFQDNs[number] = port.FQDN

		if ref, ok := portReferencedTLSSecretRef(port); ok {
			if routes.referencedTLSSecretRef != "" && routes.referencedTLSSecretRef != ref {
				return exposedPortRoutes{}, fmt.Errorf("conflicting referenced TLS Secret refs %q and %q", routes.referencedTLSSecretRef, ref)
			}
			// Own Ingress: sharing with cert-manager would fight over the secret.
			routes.referencedTLSSecretRef = ref
			routes.referencedTLSFQDNs[number] = port.FQDN
			routes.referencedTLSHosts = append(routes.referencedTLSHosts, port.FQDN)
			continue
		}

		// Standard Ingress: plain HTTP, or TLS via cert-manager.
		if port.TLS {
			routes.certManagerHosts = append(routes.certManagerHosts, port.FQDN)
		}
		routes.certManagerIngressFQDNs[number] = port.FQDN
	}
	return routes, nil
}

// portReferencedTLSSecretRef is true when the port uses a "ns/name" TLSSecretRef.
// Invalid refs fall through to cert-manager instead of dropping TLS.
func portReferencedTLSSecretRef(port v1alpha1.Port) (string, bool) {
	if !port.TLS {
		return "", false
	}
	if _, _, ok := ingresstls.SplitSecretRef(port.TLSSecretRef); !ok {
		return "", false
	}
	return port.TLSSecretRef, true
}

// buildReferencedTLSConfig builds the referenced-Secret Ingress TLS block once its replica is ready.
func buildReferencedTLSConfig(ref string, hosts []string, stage tlsStage) []networkingv1.IngressTLS {
	if ref == "" || stage != tlsStageReady {
		return nil
	}
	_, name, _ := ingresstls.SplitSecretRef(ref)
	return []networkingv1.IngressTLS{{
		Hosts:      lo.Uniq(hosts),
		SecretName: name,
	}}
}

func setTLSCondition(resource *v1alpha1.StackResource, status v1.ConditionStatus, reason, message string) {
	ingresstls.SetTLSCondition(&resource.Status.Conditions, resource.Generation, string(v1alpha1.StackResourceTLSConfigured), status, reason, message)
}

// buildIngressRules builds one rule per exposed port, ordered by port number so the
// desired Ingress is stable across reconciles and drift detection stays quiet.
func buildIngressRules(portFqdnMap map[int]string, serviceName string) []networkingv1.IngressRule {
	rules := make([]networkingv1.IngressRule, 0, len(portFqdnMap))
	for _, port := range slices.Sorted(maps.Keys(portFqdnMap)) {
		rules = append(rules, networkingv1.IngressRule{
			Host: portFqdnMap[port],
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

func referencedTLSIngressNameForResource(resourceName string) string {
	// Keep the established object name so upgrades update the existing Ingress.
	return fmt.Sprintf("%s-wildcard", resourceName)
}
