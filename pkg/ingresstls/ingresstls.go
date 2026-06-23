package ingresstls

import (
	"context"
	"fmt"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

const (
	CertManagerClusterIssuerAnnotation = "cert-manager.io/cluster-issuer"
	TraefikEntrypointsAnnotation       = "traefik.ingress.kubernetes.io/router.entrypoints"
	TraefikMiddlewaresAnnotation       = "traefik.ingress.kubernetes.io/router.middlewares"
	RedirectMiddlewareName             = "redirect-https"
)

var ManagedAnnotations = []string{
	CertManagerClusterIssuerAnnotation,
	TraefikEntrypointsAnnotation,
	TraefikMiddlewaresAnnotation,
}

// ResolveClusterIssuer reads the cluster-issuer annotation from the object,
// verifies the ClusterIssuer exists, and returns its name. Returns ("", false)
// with a reason/message pair if the issuer cannot be resolved.
func ResolveClusterIssuer(ctx context.Context, c client.Client, logger logr.Logger, annotations map[string]string) (issuerName string, ok bool, reason string, message string) {
	issuerName = annotations[corev1alpha1.ClusterIssuerAnnotation]
	if issuerName == "" {
		logger.Info("missing cluster-issuer annotation, skipping TLS")
		return "", false, "ClusterIssuerNotConfigured", fmt.Sprintf("Missing annotation %s", corev1alpha1.ClusterIssuerAnnotation)
	}

	if err := c.Get(ctx, types.NamespacedName{Name: issuerName}, &cmv1.ClusterIssuer{}); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("ClusterIssuer not found, skipping TLS", "clusterIssuer", issuerName)
			return "", false, "ClusterIssuerNotFound", fmt.Sprintf("ClusterIssuer %q does not exist", issuerName)
		}
		logger.Error(err, "failed to get ClusterIssuer", "clusterIssuer", issuerName)
		return "", false, "ClusterIssuerLookupFailed", fmt.Sprintf("Failed to look up ClusterIssuer %q: %v", issuerName, err)
	}

	return issuerName, true, "", ""
}

// BuildTLSConfig returns the annotations and TLS block for an Ingress given a
// resolved issuer name, the TLS hosts, namespace, and resource name.
func BuildTLSConfig(issuerName string, tlsHosts []string, namespace, tlsSecretName string) (annotations map[string]string, tls []networkingv1.IngressTLS) {
	annotations = map[string]string{}
	if issuerName == "" || len(tlsHosts) == 0 {
		return
	}

	annotations[CertManagerClusterIssuerAnnotation] = issuerName
	annotations[TraefikEntrypointsAnnotation] = "web,websecure"
	annotations[TraefikMiddlewaresAnnotation] = fmt.Sprintf("%s-%s@kubernetescrd", namespace, RedirectMiddlewareName)
	tls = []networkingv1.IngressTLS{{
		Hosts:      lo.Uniq(tlsHosts),
		SecretName: tlsSecretName,
	}}
	return
}

// EnsureRedirectMiddleware creates or updates the Traefik Middleware CR for
// HTTP-to-HTTPS redirect in the given namespace.
func EnsureRedirectMiddleware(ctx context.Context, c client.Client, namespace string) error {
	desired := buildRedirectMiddleware(namespace)
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	key := types.NamespacedName{Name: desired.GetName(), Namespace: desired.GetNamespace()}
	if err := c.Get(ctx, key, existing); err != nil {
		if apierrors.IsNotFound(err) {
			return c.Create(ctx, desired)
		}
		return err
	}
	if !equality.Semantic.DeepEqual(desired.Object["spec"], existing.Object["spec"]) {
		existing.Object["spec"] = desired.Object["spec"]
		return c.Update(ctx, existing)
	}
	return nil
}

// IngressNeedsUpdate checks whether the desired Ingress spec or managed
// annotations differ from the existing Ingress.
func IngressNeedsUpdate(desired, existing *networkingv1.Ingress) bool {
	if !equality.Semantic.DeepEqual(desired.Spec, existing.Spec) {
		return true
	}
	for _, key := range ManagedAnnotations {
		if desired.Annotations[key] != existing.Annotations[key] {
			return true
		}
	}
	return false
}

// SyncManagedAnnotations copies managed annotation values from desired into
// the ingress, deleting any that are empty in desired.
func SyncManagedAnnotations(ingress *networkingv1.Ingress, desired map[string]string) {
	if ingress.Annotations == nil {
		ingress.Annotations = map[string]string{}
	}
	for _, key := range ManagedAnnotations {
		if value := desired[key]; value != "" {
			ingress.Annotations[key] = value
		} else {
			delete(ingress.Annotations, key)
		}
	}
}

// SetTLSCondition sets a TLS status condition on the given conditions slice.
func SetTLSCondition(conditions *[]metav1.Condition, generation int64, conditionType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}

func buildRedirectMiddleware(namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "traefik.io/v1alpha1",
			"kind":       "Middleware",
			"metadata": map[string]interface{}{
				"name":      RedirectMiddlewareName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"redirectScheme": map[string]interface{}{
					"scheme":    "https",
					"permanent": true,
				},
			},
		},
	}
}
