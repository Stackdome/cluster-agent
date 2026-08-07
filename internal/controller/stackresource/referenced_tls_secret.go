package stackresource

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
	"stackdome.io/cluster-agent/pkg/ingresstls"
)

// tlsSecretSourceLabel marks a TLS secret this agent replicated into a workload namespace
// and records which namespace it was copied from. A TLS secret without it was not put
// there by us.
const tlsSecretSourceLabel = "core.stackdome.io/tls-secret-source"

const reasonTLSSecretUnavailable = "TLSSecretUnavailable"

// reconcileReferencedTLSSecret makes the Secret named by tlsSecretRef available in the
// workload namespace. Its status tells the Ingress whether to use HTTPS, wait, or fall
// back to HTTP. An empty reference clears the explicit tracking status.
func (r *svcReconciler) reconcileReferencedTLSSecret(
	ctx context.Context,
	resource *v1alpha1.StackResource,
	tlsSecretRef string,
) (tlsState, error) {
	if tlsSecretRef == "" {
		resource.Status.ReferencedTLSSecret = nil
		return tlsState{}, nil
	}

	sourceNamespace, name, ok := ingresstls.SplitSecretRef(tlsSecretRef)
	if !ok {
		return tlsState{
			Stage:   tlsStageUnavailable,
			Reason:  reasonTLSSecretUnavailable,
			Message: "invalid referenced TLS Secret reference",
		}, nil
	}

	ready, message, err := r.mirrorTLSSecret(ctx, resource, tlsSecretRef, types.NamespacedName{Namespace: sourceNamespace, Name: name})
	if err != nil {
		return tlsState{}, err
	}

	var status tlsState
	if !ready {
		status = referencedTLSStatusWhileBlocked(resource, tlsSecretRef, message, time.Now())
	} else {
		status = tlsState{
			Stage:   tlsStageReady,
			Reason:  reasonTLSReady,
			Message: message,
		}
	}
	updateReferencedTLSSecretStatus(resource, tlsSecretRef, status)
	return status, nil
}

func (r *svcReconciler) mirrorTLSSecret(
	ctx context.Context,
	resource *v1alpha1.StackResource,
	tlsSecretRef string,
	sourceKey types.NamespacedName,
) (ok bool, msg string, err error) {

	source := &corev1.Secret{}
	if err := r.Client.Get(ctx, sourceKey, source); err != nil {
		if !apierrors.IsNotFound(err) {
			return false, "", err
		}
		return false, fmt.Sprintf("waiting for referenced TLS Secret %s", tlsSecretRef), nil
	}

	// cert-manager can create the Secret before it contains a complete key pair.
	if len(source.Data[corev1.TLSCertKey]) == 0 || len(source.Data[corev1.TLSPrivateKeyKey]) == 0 {
		return false, fmt.Sprintf("waiting for a certificate and private key in referenced TLS Secret %s", tlsSecretRef), nil
	}

	replicaReady, err := r.ensureReferencedTLSSecretReplica(ctx, resource.Namespace, source)
	if err != nil {
		return false, "", err
	}
	if !replicaReady {
		return false, fmt.Sprintf("waiting for referenced TLS Secret replica %s to be recreated", sourceKey.Name), nil
	}
	return true, "referenced TLS Secret replicated", nil
}

func referencedTLSStatusWhileBlocked(resource *v1alpha1.StackResource, tlsSecretRef, message string, now time.Time) tlsState {
	remaining := remainingTLSGracePeriod(referencedTLSSecretPendingSince(resource, tlsSecretRef), now)
	if remaining == 0 {
		return tlsState{
			Stage:   tlsStageUnavailable,
			Reason:  reasonTLSSecretUnavailable,
			Message: fmt.Sprintf("TLS secret unavailable after %s: %s", tlsGracePeriod, message),
		}
	}
	return tlsState{
		Stage:      tlsStageWaiting,
		Reason:     reasonTLSWaiting,
		Message:    message,
		RetryAfter: remaining,
	}
}

func referencedTLSSecretPendingSince(resource *v1alpha1.StackResource, tlsSecretRef string) time.Time {
	status := resource.Status.ReferencedTLSSecret
	if status == nil || status.Reference != tlsSecretRef || status.WaitingSince == nil {
		return time.Time{}
	}
	return status.WaitingSince.Time
}

// updateReferencedTLSSecretStatus keeps the grace-period clock scoped to its Secret
// reference. TLSConfigured remains the only user-facing TLS condition.
func updateReferencedTLSSecretStatus(resource *v1alpha1.StackResource, tlsSecretRef string, status tlsState) {
	tracking := &v1alpha1.ReferencedTLSSecretStatus{Reference: tlsSecretRef}
	if status.Stage != tlsStageReady {
		existing := resource.Status.ReferencedTLSSecret
		if existing != nil && existing.Reference == tlsSecretRef && existing.WaitingSince != nil {
			tracking.WaitingSince = existing.WaitingSince
		} else {
			now := metav1.Now()
			tracking.WaitingSince = &now
		}
	}
	resource.Status.ReferencedTLSSecret = tracking
}

// ensureReferencedTLSSecretReplica creates or refreshes the copy in targetNamespace.
// Data is copied verbatim, so renewals propagate. The replica has no owner because
// many StackResources can share it; reconciliation keeps it current.
//
// It reports whether the replica is in place. A same-named secret of another type is not
// one: the type is immutable, so it is discarded (when it is ours) and this pass stays
// pending.
func (r *svcReconciler) ensureReferencedTLSSecretReplica(ctx context.Context, targetNamespace string, source *corev1.Secret) (bool, error) {
	replica := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      source.Name,
			Namespace: targetNamespace,
		},
	}

	wrongType := false
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, replica, func() error {
		if replica.ResourceVersion != "" && replica.Type != corev1.SecretTypeTLS {
			wrongType = true
			return nil
		}

		replica.Type = corev1.SecretTypeTLS
		replica.Data = source.Data
		if replica.Labels == nil {
			replica.Labels = map[string]string{}
		}
		replica.Labels[tlsSecretSourceLabel] = source.Namespace
		replica.OwnerReferences = nil
		return nil
	})
	if err != nil {
		return false, err
	}

	if wrongType {
		return false, r.deleteManagedWrongTypeReplica(ctx, replica)
	}
	if result == controllerutil.OperationResultUpdated {
		controller.LoggerFromContext(ctx).Info("updated TLS secret replica",
			"secret", controller.GetNamespacedName(replica),
			"source", controller.GetNamespacedName(source))
	}
	return true, nil
}

// deleteManagedWrongTypeReplica deletes a managed replica whose type is not TLS. A foreign
// Secret under the same name is left alone and the referenced Secret stays pending.
func (r *svcReconciler) deleteManagedWrongTypeReplica(ctx context.Context, existing *corev1.Secret) error {
	logger := controller.LoggerFromContext(ctx)

	_, labelled := existing.Labels[tlsSecretSourceLabel]
	if !labelled {
		logger.Info("a foreign Secret of another type holds the referenced TLS Secret replica name",
			"secret", controller.GetNamespacedName(existing), "type", existing.Type)
		return nil
	}

	logger.Info("deleting a managed referenced TLS Secret replica of the wrong type so it can be recreated",
		"secret", controller.GetNamespacedName(existing), "type", existing.Type)
	return client.IgnoreNotFound(r.Client.Delete(ctx, existing))
}
