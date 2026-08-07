package stackresource

import (
	"context"
	"fmt"
	"time"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/mocks"
)

var _ = Describe("classifyCertificate", func() {
	// A fixed clock keeps the elapsed-time arithmetic explicit instead of
	// depending on how long the suite takes to run.
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	justCreated := now.Add(-10 * time.Second)
	longAgo := now.Add(-10 * time.Minute)

	readyCert := func() *cmv1.Certificate {
		return &cmv1.Certificate{
			Status: cmv1.CertificateStatus{
				Conditions: []cmv1.CertificateCondition{{
					Type:    cmv1.CertificateConditionReady,
					Status:  cmmeta.ConditionTrue,
					Reason:  "Ready",
					Message: "Certificate is up to date and has not expired",
				}},
			},
		}
	}

	It("is issuing when the Certificate does not exist yet and the grace period has not elapsed", func() {
		got := classifyCertificate(nil, justCreated, now)
		Expect(got.Stage).To(Equal(tlsStageWaiting))
		Expect(got.Reason).To(Equal("CertificateIssuing"))
		Expect(got.RetryAfter).To(Equal(tlsGracePeriod - 10*time.Second))
	})

	It("is issuing when the Certificate exists but is not yet Ready", func() {
		cert := &cmv1.Certificate{
			Status: cmv1.CertificateStatus{
				Conditions: []cmv1.CertificateCondition{{
					Type:    cmv1.CertificateConditionIssuing,
					Status:  cmmeta.ConditionTrue,
					Reason:  "Requested",
					Message: "Issuing certificate as Secret does not exist",
				}},
			},
		}
		got := classifyCertificate(cert, justCreated, now)
		Expect(got.Stage).To(Equal(tlsStageWaiting))
		Expect(got.Message).To(Equal("Issuing certificate as Secret does not exist"))
		Expect(got.RetryAfter).To(BeNumerically(">", 0))
	})

	It("is ready when the Ready condition is True", func() {
		got := classifyCertificate(readyCert(), longAgo, now)
		Expect(got.Stage).To(Equal(tlsStageReady))
		Expect(got.Reason).To(Equal("TLSReady"))
		Expect(got.RetryAfter).To(BeZero())
	})

	It("stays ready past the grace period — the clock only bounds the pending window", func() {
		got := classifyCertificate(readyCert(), longAgo, now)
		Expect(got.Stage).To(Equal(tlsStageReady))
	})

	It("is failed when cert-manager records a failed issuance attempt", func() {
		cert := &cmv1.Certificate{
			Status: cmv1.CertificateStatus{
				FailedIssuanceAttempts: ptr.To(2),
				Conditions: []cmv1.CertificateCondition{{
					Type:    cmv1.CertificateConditionIssuing,
					Status:  cmmeta.ConditionFalse,
					Reason:  "Failed",
					Message: "The certificate request has failed to complete",
				}},
			},
		}
		got := classifyCertificate(cert, justCreated, now)
		Expect(got.Stage).To(Equal(tlsStageUnavailable))
		Expect(got.Reason).To(Equal("CertificateFailed"))
		Expect(got.Message).To(ContainSubstring("failed to complete"))
		Expect(got.RetryAfter).To(BeZero())
	})

	It("stays ready when a Ready cert still carries a stale failure count", func() {
		cert := readyCert()
		cert.Status.FailedIssuanceAttempts = ptr.To(1)
		got := classifyCertificate(cert, longAgo, now)
		Expect(got.Stage).To(Equal(tlsStageReady))
	})

	It("times out when the grace period elapses with no Certificate at all", func() {
		got := classifyCertificate(nil, longAgo, now)
		Expect(got.Stage).To(Equal(tlsStageUnavailable))
		Expect(got.Reason).To(Equal("CertificateTimedOut"))
		Expect(got.RetryAfter).To(BeZero())
	})

	It("times out when the grace period elapses with a Certificate stuck not-Ready", func() {
		cert := &cmv1.Certificate{
			Status: cmv1.CertificateStatus{
				Conditions: []cmv1.CertificateCondition{{
					Type:    cmv1.CertificateConditionIssuing,
					Status:  cmmeta.ConditionTrue,
					Reason:  "Requested",
					Message: "Waiting on the ACME challenge",
				}},
			},
		}
		got := classifyCertificate(cert, longAgo, now)
		Expect(got.Stage).To(Equal(tlsStageUnavailable))
		Expect(got.Message).To(ContainSubstring("ACME challenge"))
	})

	It("treats a zero pendingSince as starting the clock now, not at the epoch", func() {
		// Nothing has reported TLS as pending on the very first pass, so there is
		// no condition to read a timestamp from. Measuring elapsed time from the
		// epoch would time out instantly.
		got := classifyCertificate(nil, time.Time{}, now)
		Expect(got.Stage).To(Equal(tlsStageWaiting))
		Expect(got.RetryAfter).To(Equal(tlsGracePeriod))
	})
})

var _ = Describe("remainingTLSGracePeriod", func() {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	DescribeTable("returns the time left in the TLS grace window",
		func(pendingSince time.Time, want time.Duration) {
			Expect(remainingTLSGracePeriod(pendingSince, now)).To(Equal(want))
		},
		Entry("starts a full grace period when no pending time is recorded", time.Time{}, tlsGracePeriod),
		Entry("returns the active remainder", now.Add(-10*time.Second), tlsGracePeriod-10*time.Second),
		Entry("expires at the grace boundary", now.Add(-tlsGracePeriod), time.Duration(0)),
		Entry("stays expired after the grace boundary", now.Add(-2*tlsGracePeriod), time.Duration(0)),
	)
})

var _ = Describe("cert-manager TLS status", func() {
	It("starts the grace period when certificate issuance begins", func() {
		resource := &v1alpha1.StackResource{}

		updateCertManagerTLSStatus(resource, tlsState{Stage: tlsStageWaiting})

		Expect(resource.Status.CertManagerTLS).NotTo(BeNil())
		Expect(resource.Status.CertManagerTLS.WaitingSince).NotTo(BeNil())
		Expect(resource.Status.CertManagerTLS.WaitingSince.Time).To(BeTemporally("~", time.Now(), time.Second))
	})

	It("preserves the grace-period start across pending reconciles", func() {
		waitingSince := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
		resource := &v1alpha1.StackResource{
			Status: v1alpha1.StackResourceStatus{
				CertManagerTLS: &v1alpha1.CertManagerTLSStatus{WaitingSince: &waitingSince},
			},
		}

		updateCertManagerTLSStatus(resource, tlsState{Stage: tlsStageWaiting})

		Expect(certManagerTLSPendingSince(resource)).To(Equal(waitingSince.Time))
	})

	It("keeps the expired grace-period start while TLS is unavailable", func() {
		waitingSince := metav1.NewTime(time.Now().Add(-tlsGracePeriod).Truncate(time.Second))
		resource := &v1alpha1.StackResource{
			Status: v1alpha1.StackResourceStatus{
				CertManagerTLS: &v1alpha1.CertManagerTLSStatus{WaitingSince: &waitingSince},
			},
		}

		updateCertManagerTLSStatus(resource, tlsState{Stage: tlsStageUnavailable})

		Expect(certManagerTLSPendingSince(resource)).To(Equal(waitingSince.Time))
	})

	DescribeTable("clears the grace-period clock when cert-manager TLS is not waiting",
		func(stage tlsStage) {
			waitingSince := metav1.NewTime(time.Now().Add(-time.Minute))
			resource := &v1alpha1.StackResource{
				Status: v1alpha1.StackResourceStatus{
					CertManagerTLS: &v1alpha1.CertManagerTLSStatus{WaitingSince: &waitingSince},
				},
			}

			updateCertManagerTLSStatus(resource, tlsState{Stage: stage})

			Expect(resource.Status.CertManagerTLS).To(BeNil())
		},
		Entry("certificate is ready", tlsStageReady),
		Entry("no cert-manager TLS ports exist", tlsStageNone),
	)
})

var _ = Describe("getCertManagerTLSState", func() {
	var (
		mockCtrl   *gomock.Controller
		mockClient *mocks.MockClient
		reconciler *svcReconciler
		resource   *v1alpha1.StackResource
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mocks.NewMockClient(mockCtrl)
		reconciler = &svcReconciler{Client: mockClient}
		resource = &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "test-ns"},
		}
	})

	AfterEach(func() { mockCtrl.Finish() })

	It("looks the Certificate up as <resource>-tls in the resource namespace", func() {
		mockClient.EXPECT().
			Get(gomock.Any(), client.ObjectKey{Name: "my-app-tls", Namespace: "test-ns"}, gomock.AssignableToTypeOf(&cmv1.Certificate{})).
			DoAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				*obj.(*cmv1.Certificate) = cmv1.Certificate{
					Status: cmv1.CertificateStatus{
						Conditions: []cmv1.CertificateCondition{{
							Type:   cmv1.CertificateConditionReady,
							Status: cmmeta.ConditionTrue,
						}},
					},
				}
				return nil
			})

		got, err := reconciler.getCertManagerTLSState(context.Background(), resource)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Stage).To(Equal(tlsStageReady))
	})

	It("treats a missing Certificate as not-yet-created rather than an error", func() {
		mockClient.EXPECT().
			Get(gomock.Any(), client.ObjectKey{Name: "my-app-tls", Namespace: "test-ns"}, gomock.AssignableToTypeOf(&cmv1.Certificate{})).
			Return(apierrors.NewNotFound(schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}, "my-app-tls"))

		got, err := reconciler.getCertManagerTLSState(context.Background(), resource)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Stage).To(Equal(tlsStageWaiting))
	})

	It("propagates a non-NotFound Get error", func() {
		mockClient.EXPECT().
			Get(gomock.Any(), gomock.Any(), gomock.AssignableToTypeOf(&cmv1.Certificate{})).
			Return(apierrors.NewInternalError(fmt.Errorf("boom")))

		_, err := reconciler.getCertManagerTLSState(context.Background(), resource)
		Expect(err).To(HaveOccurred())
	})
})
