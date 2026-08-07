package stackresource

import (
	"context"
	"fmt"
	"strings"
	"time"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

// tlsStage describes whether one TLS path can serve HTTPS. It controls the
// redirect annotation, the published URL, and TLSConfigured.
type tlsStage string

const (
	// tlsStageNone is the zero value: this TLS path is not in use.
	tlsStageNone tlsStage = ""
	// tlsStageWaiting means TLS is not ready and the grace period is active.
	// No URL is published during this period.
	tlsStageWaiting tlsStage = "Waiting"
	// tlsStageReady means HTTPS is ready.
	tlsStageReady tlsStage = "Ready"
	// tlsStageUnavailable means TLS failed or the grace period expired. The
	// address falls back to HTTP until a later event makes TLS ready.
	tlsStageUnavailable tlsStage = "Unavailable"
)

// reasonTLSReady is used when either TLS path can serve HTTPS.
const reasonTLSReady = "TLSReady"

// Keep the existing reason value because the hub renders it as "TLS issuing".
const reasonTLSWaiting = "CertificateIssuing"

// tlsGracePeriod limits how long a TLS port has no published URL. After this
// period it uses HTTP until its certificate or referenced Secret becomes ready.
const tlsGracePeriod = 2 * time.Minute

// remainingTLSGracePeriod returns how much of the TLS pending window remains.
// A missing pending time starts a fresh full window; an expired window has no
// remaining time.
func remainingTLSGracePeriod(pendingSince, now time.Time) time.Duration {
	if pendingSince.IsZero() {
		return tlsGracePeriod
	}
	remaining := tlsGracePeriod - now.Sub(pendingSince)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// tlsState is the result of checking one TLS path. RetryAfter schedules the
// reconcile that turns a stalled path into HTTP fallback.
type tlsState struct {
	Stage      tlsStage
	Reason     string
	Message    string
	RetryAfter time.Duration
}

// classifyCertificate decides the stage from the Certificate alone. A nil cert
// means ingress-shim has not created it yet. pendingSince is when this resource
// first reported TLS as not-ready and starts the grace-period clock; a zero
// value means the clock starts now. It is pure so the time arithmetic is
// testable without a clock abstraction.
func classifyCertificate(cert *cmv1.Certificate, pendingSince time.Time, now time.Time) tlsState {
	var issuingMsg string
	if cert != nil {
		if cond := findCertCondition(cert, cmv1.CertificateConditionReady); cond != nil && cond.Status == cmmeta.ConditionTrue {
			return tlsState{
				Stage:   tlsStageReady,
				Reason:  reasonTLSReady,
				Message: orDefault(condMessage(cond), "certificate issued"),
			}
		}
		issuingMsg = condMessage(findCertCondition(cert, cmv1.CertificateConditionIssuing))

		// FailedIssuanceAttempts is cert-manager's own count of continuous failed
		// attempts, cleared on success. It survives where the Issuing condition
		// does not — that condition is removed outright once issuance completes.
		if n := cert.Status.FailedIssuanceAttempts; n != nil && *n > 0 {
			return tlsState{
				Stage:   tlsStageUnavailable,
				Reason:  "CertificateFailed",
				Message: fmt.Sprintf("%s (%d failed attempts)", orDefault(issuingMsg, "certificate issuance failed"), *n),
			}
		}
	}

	pendingMsg := orDefault(issuingMsg, "waiting for cert-manager to issue the certificate")
	remaining := remainingTLSGracePeriod(pendingSince, now)
	if remaining == 0 {
		return tlsState{
			Stage:   tlsStageUnavailable,
			Reason:  "CertificateTimedOut",
			Message: fmt.Sprintf("no certificate after %s: %s", tlsGracePeriod, pendingMsg),
		}
	}

	return tlsState{
		Stage:      tlsStageWaiting,
		Reason:     reasonTLSWaiting,
		Message:    pendingMsg,
		RetryAfter: remaining,
	}
}

func certManagerTLSPendingSince(resource *v1alpha1.StackResource) time.Time {
	status := resource.Status.CertManagerTLS
	if status == nil || status.WaitingSince == nil {
		return time.Time{}
	}
	return status.WaitingSince.Time
}

func updateCertManagerTLSStatus(resource *v1alpha1.StackResource, status tlsState) {
	if status.Stage == tlsStageNone || status.Stage == tlsStageReady {
		resource.Status.CertManagerTLS = nil
		return
	}
	if resource.Status.CertManagerTLS != nil && resource.Status.CertManagerTLS.WaitingSince != nil {
		return
	}
	now := metav1.Now()
	resource.Status.CertManagerTLS = &v1alpha1.CertManagerTLSStatus{WaitingSince: &now}
}

// getCertManagerTLSState fetches the Certificate that cert-manager's ingress-shim
// creates for the resource's Ingress and classifies it. ingress-shim names the
// Certificate after the Ingress TLS secretName, which is <resource>-tls.
func (r *svcReconciler) getCertManagerTLSState(
	ctx context.Context,
	resource *v1alpha1.StackResource,
) (tlsState, error) {
	cert := &cmv1.Certificate{}
	key := types.NamespacedName{Name: certificateNameForResource(resource.Name), Namespace: resource.Namespace}
	if err := r.Client.Get(ctx, key, cert); err != nil {
		if !apierrors.IsNotFound(err) {
			return tlsState{}, err
		}
		cert = nil
	}
	return classifyCertificate(cert, certManagerTLSPendingSince(resource), time.Now()), nil
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
