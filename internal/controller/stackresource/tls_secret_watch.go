package stackresource

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

const DefaultPlatformTLSNamespace = "stackdome-control-plane"

// platformWildcardTLSSecretToStackResources maps the shared wildcard Secret to its consumers.
// Only metadata is fetched because reconciliation needs the resource key, not its body.
func (r *StackResourceReconciler) platformWildcardTLSSecretToStackResources(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := platformWildcardTLSSecretFrom(obj, r.platformTLSNamespace)
	if !ok {
		return nil
	}

	resources := &metav1.PartialObjectMetadataList{TypeMeta: metav1.TypeMeta{
		APIVersion: v1alpha1.GroupVersion.String(),
		Kind:       "StackResourceList",
	}}
	if err := r.uncachedClient.List(ctx, resources, client.MatchingLabels{
		v1alpha1.LabelUsesPlatformWildcardTLS: "true",
	}); err != nil {
		log.FromContext(ctx).Error(err, "listing StackResources that use the platform wildcard TLS Secret", "secret", controller.GetNamespacedName(secret))
		return nil
	}

	requests := make([]reconcile.Request, 0, len(resources.Items))
	for i := range resources.Items {
		resource := &resources.Items[i]
		requests = append(requests, reconcile.Request{NamespacedName: controller.GetNamespacedName(resource)})
	}
	return requests
}

func platformWildcardTLSSecretFrom(obj client.Object, namespace string) (*corev1.Secret, bool) {
	secret, ok := obj.(*corev1.Secret)
	if !ok || secret.Namespace != namespace || secret.Type != corev1.SecretTypeTLS {
		return nil, false
	}
	return secret, secret.Labels[v1alpha1.LabelPlatformWildcardTLSSecret] == "true"
}

func platformWildcardTLSSecretPredicate(namespace string) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		_, ok := platformWildcardTLSSecretFrom(obj, namespace)
		return ok
	})
}
