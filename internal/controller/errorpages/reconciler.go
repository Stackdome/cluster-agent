package errorpages

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// ServiceName is the cluster-wide singleton Service that routes error-page
	// traffic to the agent's own pods.
	ServiceName = "stackdome-error-pages"
	// MiddlewareName is the Traefik Middleware that swaps a 5xx response from any
	// backend for one of our pages. Stack ingresses reference it as
	// "<namespace>-stackdome-errors@kubernetescrd".
	MiddlewareName = "stackdome-errors"
	// IngressRouteName is the lowest-priority route that catches hostnames and
	// paths no real stack route claims, so visitors get our 404 page.
	IngressRouteName = "stackdome-catch-all"

	// ContainerPort is the port the agent's error-page server listens on inside
	// the pod. It must agree with DefaultAddr and with the container port
	// declared in the agent's Deployment.
	ContainerPort = 8082
	// servicePort is the port the Service publishes; Traefik objects reference it.
	servicePort = 80
)

// traefikAPIVersion is the Traefik CRD group/version. The objects are built as
// unstructured so the agent does not take on a Traefik Go dependency, matching
// pkg/ingresstls.
const traefikAPIVersion = "traefik.io/v1alpha1"

// managedLabels marks every object this package owns, so an operator can tell
// at a glance which objects the agent maintains.
func managedLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "stackdome-error-pages",
		"app.kubernetes.io/managed-by": "stackdome-operator",
	}
}

// Ensure creates or updates the Service, Middleware, and IngressRoute that put
// the agent's embedded error pages in front of Traefik. It is idempotent and
// safe to call on every agent start.
//
// selector must be the agent Deployment's own pod selector: the Service points
// at the agent, which serves the pages itself.
func Ensure(ctx context.Context, c client.Client, namespace string, selector map[string]string) error {
	// An empty selector is not "select nothing" — a Service with a non-nil but
	// empty selector matches every pod in the namespace, so the error pages
	// would be load balanced across whatever else runs alongside the agent.
	// Refusing is the only safe answer; the caller retries, and a
	// misconfigured --error-pages-selector shows up as a loud, repeated error
	// rather than as traffic silently landing on the wrong pods.
	if len(selector) == 0 {
		return fmt.Errorf("refusing to create error page Service in %s: empty pod selector would match every pod in the namespace", namespace)
	}
	if err := ensureService(ctx, c, namespace, selector); err != nil {
		return fmt.Errorf("ensuring error page Service: %w", err)
	}
	if err := ensureUnstructured(ctx, c, buildMiddleware(namespace)); err != nil {
		return fmt.Errorf("ensuring error page Middleware: %w", err)
	}
	if err := ensureUnstructured(ctx, c, buildIngressRoute(namespace)); err != nil {
		return fmt.Errorf("ensuring error page IngressRoute: %w", err)
	}
	return nil
}

func ensureService(ctx context.Context, c client.Client, namespace string, selector map[string]string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: ServiceName, Namespace: namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, c, svc, func() error {
		svc.Labels = managedLabels()
		svc.Spec.Selector = selector
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "http",
			Port:       servicePort,
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstr.FromInt32(ContainerPort),
		}}
		return nil
	})
	return err
}

// ensureUnstructured applies the desired spec of a Traefik CR, leaving any other
// field on an existing object untouched.
func ensureUnstructured(ctx context.Context, c client.Client, desired *unstructured.Unstructured) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(desired.GroupVersionKind())
	obj.SetName(desired.GetName())
	obj.SetNamespace(desired.GetNamespace())

	_, err := controllerutil.CreateOrUpdate(ctx, c, obj, func() error {
		obj.SetLabels(managedLabels())
		obj.Object["spec"] = desired.Object["spec"]
		return nil
	})
	return err
}

func buildMiddleware(namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": traefikAPIVersion,
			"kind":       "Middleware",
			"metadata": map[string]interface{}{
				"name":      MiddlewareName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"errors": map[string]interface{}{
					"status": []interface{}{"500-599"},
					"service": map[string]interface{}{
						"name": ServiceName,
						"port": int64(servicePort),
					},
					// Traefik substitutes the real status code, so one page per
					// status is served from a single middleware.
					"query": "/{status}",
				},
			},
		},
	}
}

func buildIngressRoute(namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": traefikAPIVersion,
			"kind":       "IngressRoute",
			"metadata": map[string]interface{}{
				"name":      IngressRouteName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"entryPoints": []interface{}{"web", "websecure"},
				"routes": []interface{}{
					map[string]interface{}{
						"match": "PathPrefix(`/`)",
						"kind":  "Rule",
						// Priority 1 is the lowest Traefik accepts: every real
						// stack route outranks it, so this only catches traffic
						// nothing else claims.
						"priority": int64(1),
						"services": []interface{}{
							map[string]interface{}{
								"name": ServiceName,
								"port": int64(servicePort),
							},
						},
					},
				},
			},
		},
	}
}
