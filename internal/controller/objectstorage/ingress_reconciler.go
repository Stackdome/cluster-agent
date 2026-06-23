package objectstorage

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

const defaultClusterIssuer = "letsencrypt-prod"

type ingressReconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

func newIngressReconciler(c client.Client, scheme *runtime.Scheme) *ingressReconciler {
	return &ingressReconciler{client: c, scheme: scheme}
}

func (r *ingressReconciler) name() string { return "ingress-reconciler" }

func (r *ingressReconciler) reconcile(ctx context.Context, resource *storagev1alpha1.ObjectStorage) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)

	pathType := networkingv1.PathTypePrefix

	desiredIngress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        resource.IngressName(),
			Namespace:   resource.Namespace,
			Annotations: map[string]string{},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: resource.Spec.Ingress.IngressClassName,
			Rules: []networkingv1.IngressRule{
				{
					Host: resource.Spec.Ingress.Hostname,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: resource.ServiceName(),
											Port: networkingv1.ServiceBackendPort{Number: 7480},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if resource.Spec.Ingress.TLS {
		desiredIngress.Annotations["cert-manager.io/cluster-issuer"] = defaultClusterIssuer
		desiredIngress.Spec.TLS = []networkingv1.IngressTLS{
			{
				Hosts:      []string{resource.Spec.Ingress.Hostname},
				SecretName: resource.IngressName() + "-tls",
			},
		}
	}

	if err := controllerutil.SetControllerReference(resource, desiredIngress, r.scheme); err != nil {
		return resultNil, err
	}

	existingIngress := &networkingv1.Ingress{}
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(desiredIngress), existingIngress); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating Ingress", "name", desiredIngress.Name)
			return resultRequeue, r.client.Create(ctx, desiredIngress)
		}
		return resultNil, err
	}

	scheme := "https"
	if !resource.Spec.Ingress.TLS {
		scheme = "http"
	}
	resource.Status.ExternalEndpoint = fmt.Sprintf("%s://%s", scheme, resource.Spec.Ingress.Hostname)
	return resultNil, nil
}
