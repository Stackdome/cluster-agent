package stackresource

import (
	"context"
	"fmt"
	"strings"
	"time"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

// certIssuanceStage is where a resource's TLS certificate is in its lifecycle. It
// decides three things: whether the Traefik redirect annotation goes onto the
// Ingress, what scheme (if any) a TLS port publishes as its external address,
// and what TLSConfigured reports.
type certIssuanceStage string

const (
	// certIssuanceStageIssuing means no certificate yet, but it is still early enough
	// that one is plausibly on its way. Users get no URL at all — sending them
	// to HTTPS now means the self-signed Traefik default, and a browser that
	// accepts it pins that security state for the profile.
	certIssuanceStageIssuing certIssuanceStage = "Issuing"
	// certIssuanceStageReady means the certificate is issued and valid.
	certIssuanceStageReady certIssuanceStage = "Ready"
	// certIssuanceStageUnavailable means no certificate is coming: cert-manager recorded
	// a failed attempt, or the grace period elapsed with nothing to show for it.
	// Both behave identically — fall back to HTTP so the site stays reachable —
	// and the certStatus Reason says which one it was.
	certIssuanceStageUnavailable certIssuanceStage = "Unavailable"
)

// certGracePeriod bounds how long a TLS port goes without any published
// address while waiting on issuance. HTTP-01 usually completes in 10-30
// seconds.
//
// ponytail: a fixed 2 minutes. DNS-01 issuance can legitimately take longer,
// in which case the address publishes as http:// at expiry and flips to
// https:// when the certificate lands. If that flap becomes a problem, make
// this per-resource rather than raising it globally — a longer global grace
// leaves HTTP-01 users with no URL at all for minutes in the stall case.
const certGracePeriod = 2 * time.Minute

// certStatus is the classification result. RetryAfter is non-zero only in the
// issuing stage and holds the time left until the grace period expires, so the
// reconciler can schedule a pass that will see the timeout.
type certStatus struct {
	Stage      certIssuanceStage
	Reason     string
	Message    string
	RetryAfter time.Duration
}

// classifyCertificate decides the stage from the Certificate alone. A nil cert
// means ingress-shim has not created it yet. pendingSince is when this resource
// first reported TLS as not-ready and starts the grace-period clock; a zero
// value means the clock starts now. It is pure so the time arithmetic is
// testable without a clock abstraction.
func classifyCertificate(cert *cmv1.Certificate, pendingSince time.Time, now time.Time) certStatus {
	var issuingMsg string
	if cert != nil {
		if cond := findCertCondition(cert, cmv1.CertificateConditionReady); cond != nil && cond.Status == cmmeta.ConditionTrue {
			return certStatus{
				Stage:   certIssuanceStageReady,
				Reason:  "TLSReady",
				Message: orDefault(condMessage(cond), "certificate issued"),
			}
		}
		issuingMsg = condMessage(findCertCondition(cert, cmv1.CertificateConditionIssuing))

		// FailedIssuanceAttempts is cert-manager's own count of continuous failed
		// attempts, cleared on success. It survives where the Issuing condition
		// does not — that condition is removed outright once issuance completes.
		if n := cert.Status.FailedIssuanceAttempts; n != nil && *n > 0 {
			return certStatus{
				Stage:   certIssuanceStageUnavailable,
				Reason:  "CertificateFailed",
				Message: fmt.Sprintf("%s (%d failed attempts)", orDefault(issuingMsg, "certificate issuance failed"), *n),
			}
		}
	}

	// A zero pendingSince means nothing has reported TLS as pending yet, so this
	// is the first pass. Measuring from the epoch would time out instantly.
	elapsed := time.Duration(0)
	if !pendingSince.IsZero() {
		elapsed = now.Sub(pendingSince)
	}

	pendingMsg := orDefault(issuingMsg, "waiting for cert-manager to issue the certificate")
	if elapsed >= certGracePeriod {
		return certStatus{
			Stage:   certIssuanceStageUnavailable,
			Reason:  "CertificateTimedOut",
			Message: fmt.Sprintf("no certificate after %s: %s", certGracePeriod, pendingMsg),
		}
	}

	return certStatus{
		Stage:      certIssuanceStageIssuing,
		Reason:     "CertificateIssuing",
		Message:    pendingMsg,
		RetryAfter: certGracePeriod - elapsed,
	}
}

// tlsPendingSince is when the resource first reported TLS as not-ready, which
// starts the grace-period clock. meta.SetStatusCondition only moves
// LastTransitionTime when Status changes, so this stays pinned at the start of
// the pending window however many times the reason is rewritten.
func tlsPendingSince(resource *v1alpha1.StackResource) time.Time {
	cond := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceTLSConfigured))
	if cond == nil || cond.Status != metav1.ConditionFalse {
		return time.Time{}
	}
	return cond.LastTransitionTime.Time
}

// certificateStatus fetches the Certificate that cert-manager's ingress-shim
// creates for the resource's Ingress and classifies it. ingress-shim names the
// Certificate after the Ingress TLS secretName, which is <resource>-tls.
func (r *svcReconciler) certificateStatus(
	ctx context.Context,
	resource *v1alpha1.StackResource,
) (certStatus, error) {
	cert := &cmv1.Certificate{}
	key := types.NamespacedName{Name: certificateNameForResource(resource.Name), Namespace: resource.Namespace}
	if err := r.Client.Get(ctx, key, cert); err != nil {
		if !apierrors.IsNotFound(err) {
			return certStatus{}, err
		}
		cert = nil
	}
	return classifyCertificate(cert, tlsPendingSince(resource), time.Now()), nil
}

// certNameSuffix is what ingress-shim appends when naming the Certificate and
// its Secret after the resource. certificateNameForResource applies it and
// certificateToStackResource inverts it, so the two must stay in step.
const certNameSuffix = "-tls"

// certificateNameForResource is the Certificate and TLS Secret name for a
// resource. Both are the same string by ingress-shim's naming rule.
func certificateNameForResource(resourceName string) string {
	return resourceName + certNameSuffix
}

// certificateToStackResource maps a cert-manager Certificate back to the
// StackResource whose Ingress produced it. There is no ownerRef chain to
// follow: the Certificate is owned by the Ingress, not by the StackResource,
// so the naming rule is the only link.
func certificateToStackResource(_ context.Context, obj client.Object) []reconcile.Request {
	cert, ok := obj.(*cmv1.Certificate)
	if !ok {
		return nil
	}
	name, found := strings.CutSuffix(cert.GetName(), certNameSuffix)
	if !found || name == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: name, Namespace: cert.GetNamespace()},
	}}
}

func findCertCondition(cert *cmv1.Certificate, condType cmv1.CertificateConditionType) *cmv1.CertificateCondition {
	if cert == nil {
		return nil
	}
	for i := range cert.Status.Conditions {
		if cert.Status.Conditions[i].Type == condType {
			return &cert.Status.Conditions[i]
		}
	}
	return nil
}

func condMessage(cond *cmv1.CertificateCondition) string {
	if cond == nil {
		return ""
	}
	return cond.Message
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
