package objectstorage

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/pkg/ingresstls"
)

type ingressReconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

func newIngressReconciler(c client.Client, scheme *runtime.Scheme) *ingressReconciler {
	return &ingressReconciler{client: c, scheme: scheme}
}

func (r *ingressReconciler) name() string { return "ingress-reconciler" }

func (r *ingressReconciler) reconcile(ctx context.Context, resource *storagev1alpha1.ObjectStorage) (subReconcilerResult, error) {
	ingress := resource.Spec.Ingress
	if ingress == nil {
		return resultNil, nil
	}

	annotations, tls := r.resolveTLS(ctx, resource)

	desired := buildDesiredIngress(resource, annotations, tls)
	if err := controllerutil.SetControllerReference(resource, desired, r.scheme); err != nil {
		return resultNil, err
	}

	created, err := r.syncIngress(ctx, desired, annotations)
	if err != nil {
		return resultNil, err
	}
	if err := r.finalizeTLS(ctx, resource, annotations, tls); err != nil {
		return resultNil, err
	}
	if created {
		return resultRequeue, nil
	}

	resource.Status.ExternalEndpoint = externalEndpoint(ingress.TLS, ingress.Hostname)
	return resultNil, nil
}

func (r *ingressReconciler) resolveTLS(ctx context.Context, resource *storagev1alpha1.ObjectStorage) (map[string]string, []networkingv1.IngressTLS) {
	if !resource.Spec.Ingress.TLS {
		return map[string]string{}, nil
	}

	logger := log.FromContext(ctx)
	issuerName, ok, reason, message := ingresstls.ResolveClusterIssuer(ctx, r.client, logger, resource.Annotations)
	if !ok {
		ingresstls.SetTLSCondition(
			&resource.Status.Conditions,
			resource.Generation,
			storagev1alpha1.ObjectStorageConditionTLSConfigured,
			metav1.ConditionFalse,
			reason,
			message,
		)
		return map[string]string{}, nil
	}

	return ingresstls.BuildTLSConfig(
		issuerName,
		[]string{resource.Spec.Ingress.Hostname},
		resource.Namespace,
		resource.IngressName()+"-tls",
	)
}

func buildDesiredIngress(resource *storagev1alpha1.ObjectStorage, annotations map[string]string, tls []networkingv1.IngressTLS) *networkingv1.Ingress {
	ingress := resource.Spec.Ingress
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        resource.IngressName(),
			Namespace:   resource.Namespace,
			Annotations: annotations,
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: ingress.IngressClassName,
			TLS:              tls,
			Rules: []networkingv1.IngressRule{{
				Host: ingress.Hostname,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     "/",
							PathType: ptr.To(networkingv1.PathTypePrefix),
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: resource.ServiceName(),
									Port: networkingv1.ServiceBackendPort{Number: storagev1alpha1.ObjectStorageContainerPort},
								},
							},
						}},
					},
				},
			}},
		},
	}
}

func (r *ingressReconciler) syncIngress(ctx context.Context, desired *networkingv1.Ingress, annotations map[string]string) (created bool, err error) {
	existing := &networkingv1.Ingress{}
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
		if apierrors.IsNotFound(err) {
			log.FromContext(ctx).Info("Creating Ingress", "name", desired.Name)
			return true, r.client.Create(ctx, desired)
		}
		return false, err
	}

	if !ingresstls.IngressNeedsUpdate(desired, existing) {
		return false, nil
	}

	existing.Spec = desired.Spec
	ingresstls.SyncManagedAnnotations(existing, annotations)
	return false, r.client.Update(ctx, existing)
}

func (r *ingressReconciler) finalizeTLS(ctx context.Context, resource *storagev1alpha1.ObjectStorage, annotations map[string]string, tls []networkingv1.IngressTLS) error {
	if len(tls) > 0 {
		if err := ingresstls.EnsureRedirectMiddleware(ctx, r.client, resource.Namespace); err != nil {
			return err
		}
	}
	r.setTLSConfiguredIfEnabled(resource, annotations)
	return nil
}

func externalEndpoint(tlsEnabled bool, hostname string) string {
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, hostname)
}

func (r *ingressReconciler) setTLSConfiguredIfEnabled(resource *storagev1alpha1.ObjectStorage, annotations map[string]string) {
	issuerName := annotations[ingresstls.CertManagerClusterIssuerAnnotation]
	if issuerName != "" {
		ingresstls.SetTLSCondition(
			&resource.Status.Conditions,
			resource.Generation,
			storagev1alpha1.ObjectStorageConditionTLSConfigured,
			metav1.ConditionTrue,
			"TLSConfigured",
			fmt.Sprintf("Using ClusterIssuer %q", issuerName),
		)
	}
}
