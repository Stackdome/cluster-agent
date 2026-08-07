package stackresource

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

const (
	sourceSecretNamespace  = "stackdome-system"
	sourceSecretName       = "platform-wildcard-tls"
	referencedTLSSecretRef = sourceSecretNamespace + "/" + sourceSecretName
)

func findTLSCondition(resource *v1alpha1.StackResource) *metav1.Condition {
	return meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceTLSConfigured))
}

// sourceTLSSecret is the platform wildcard secret as it exists in its own namespace.
func sourceTLSSecret(namespace, name, cert string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       []byte(cert),
			corev1.TLSPrivateKeyKey: []byte("key-" + cert),
		},
	}
}

var _ = Describe("svcReconciler referenced TLS Secret reconciliation", func() {
	var (
		ctx        context.Context
		scheme     *runtime.Scheme
		reconciler *svcReconciler
		resource   *v1alpha1.StackResource
	)

	newReconciler := func(objects ...client.Object) {
		reconciler = &svcReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
			Scheme: scheme,
		}
	}

	replicaIn := func(namespace string) *corev1.Secret {
		replica := &corev1.Secret{}
		key := types.NamespacedName{Namespace: namespace, Name: sourceSecretName}
		Expect(reconciler.Client.Get(ctx, key, replica)).To(Succeed())
		return replica
	}

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		resource = newSvcTestResource(nil, nil)
	})

	It("copies the source secret into the workload namespace under the same name", func() {
		newReconciler(sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first"))

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageReady))
		Expect(status.Reason).To(Equal(reasonTLSReady))

		replica := replicaIn(resource.Namespace)
		Expect(replica.Type).To(Equal(corev1.SecretTypeTLS))
		Expect(replica.Data).To(HaveKeyWithValue(corev1.TLSCertKey, []byte("first")))
		Expect(replica.Data).To(HaveKeyWithValue(corev1.TLSPrivateKeyKey, []byte("key-first")))

		By("stamping the copy so it is findable and traceable to its source")
		Expect(replica.Labels).To(HaveKeyWithValue(tlsSecretSourceLabel, sourceSecretNamespace))
		Expect(replica.OwnerReferences).To(BeEmpty())
	})

	It("propagates a renewed source certificate to the replica", func() {
		source := sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first")
		newReconciler(source)

		_, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())

		source.Data[corev1.TLSCertKey] = []byte("renewed")
		Expect(reconciler.Client.Update(ctx, source)).To(Succeed())

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageReady))
		Expect(replicaIn(resource.Namespace).Data).To(HaveKeyWithValue(corev1.TLSCertKey, []byte("renewed")))
	})

	It("keeps a shared replica independent of resource ownership", func() {
		newReconciler(sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first"))
		_, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())

		sibling := newSvcTestResource(nil, nil)
		sibling.Name = "my-other-app"
		sibling.UID = "other-uid"
		_, err = reconciler.reconcileReferencedTLSSecret(ctx, sibling, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())

		Expect(replicaIn(resource.Namespace).OwnerReferences).To(BeEmpty())
	})

	It("waits with reason CertificateIssuing while the source secret is missing", func() {
		newReconciler()

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageWaiting))
		Expect(status.Reason).To(Equal(reasonTLSWaiting))
		Expect(status.Message).To(ContainSubstring(referencedTLSSecretRef))
		Expect(status.RetryAfter).To(BeNumerically(">", 0))

		replica := &corev1.Secret{}
		key := types.NamespacedName{Namespace: resource.Namespace, Name: sourceSecretName}
		Expect(reconciler.Client.Get(ctx, key, replica)).NotTo(Succeed())
	})

	It("records the referenced secret and starts its grace period while blocked", func() {
		newReconciler()

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)

		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageWaiting))
		Expect(resource.Status.ReferencedTLSSecret).NotTo(BeNil())
		Expect(resource.Status.ReferencedTLSSecret.Reference).To(Equal(referencedTLSSecretRef))
		Expect(resource.Status.ReferencedTLSSecret.WaitingSince).NotTo(BeNil())
		Expect(resource.Status.ReferencedTLSSecret.WaitingSince.Time).To(BeTemporally("~", time.Now(), time.Second))
	})

	It("starts a fresh grace period when the referenced secret changes", func() {
		newReconciler()
		firstRef := "stackdome-system/first-wildcard-tls"
		secondRef := "stackdome-system/second-wildcard-tls"

		_, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, firstRef)
		Expect(err).NotTo(HaveOccurred())
		resource.Status.ReferencedTLSSecret.WaitingSince = &metav1.Time{Time: time.Now().Add(-tlsGracePeriod)}

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, secondRef)

		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageWaiting))
		Expect(status.RetryAfter).To(BeNumerically(">", tlsGracePeriod-time.Second))
		Expect(resource.Status.ReferencedTLSSecret.Reference).To(Equal(secondRef))
		Expect(resource.Status.ReferencedTLSSecret.WaitingSince.Time).To(BeTemporally(">", time.Now().Add(-time.Second)))
	})

	It("preserves the waiting time across blocked reconciles for the same referenced secret", func() {
		newReconciler()
		waitingSince := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))

		_, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		resource.Status.ReferencedTLSSecret.WaitingSince = &waitingSince

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)

		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageWaiting))
		Expect(resource.Status.ReferencedTLSSecret.WaitingSince).NotTo(BeNil())
		Expect(resource.Status.ReferencedTLSSecret.WaitingSince.Time).To(Equal(waitingSince.Time))
	})

	It("clears the waiting time once the referenced secret replica is ready", func() {
		newReconciler()
		_, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(resource.Status.ReferencedTLSSecret.WaitingSince).NotTo(BeNil())

		Expect(reconciler.Client.Create(ctx, sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first"))).To(Succeed())
		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)

		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageReady))
		Expect(resource.Status.ReferencedTLSSecret).To(Equal(&v1alpha1.ReferencedTLSSecretStatus{Reference: referencedTLSSecretRef}))
	})

	It("clears referenced secret tracking when no reference is configured", func() {
		newReconciler()
		resource.Status.ReferencedTLSSecret = &v1alpha1.ReferencedTLSSecretStatus{
			Reference:    referencedTLSSecretRef,
			WaitingSince: &metav1.Time{Time: time.Now().Add(-time.Minute)},
		}

		_, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, "")

		Expect(err).NotTo(HaveOccurred())
		Expect(resource.Status.ReferencedTLSSecret).To(BeNil())
	})

	It("starts a new grace period when the source disappears after it was ready", func() {
		source := sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first")
		newReconciler(source)
		_, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(resource.Status.ReferencedTLSSecret).To(Equal(&v1alpha1.ReferencedTLSSecretStatus{Reference: referencedTLSSecretRef}))

		Expect(reconciler.Client.Delete(ctx, source)).To(Succeed())
		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)

		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageWaiting))
		Expect(resource.Status.ReferencedTLSSecret.WaitingSince).NotTo(BeNil())
		Expect(resource.Status.ReferencedTLSSecret.WaitingSince.Time).To(BeTemporally("~", time.Now(), time.Second))
	})

	It("starts a full grace period despite an old custom TLS condition", func() {
		newReconciler()
		resource.Spec.Ports = []v1alpha1.Port{
			{Name: "platform", Number: 443, ExposeToPublic: true, TLS: true, TLSSecretRef: referencedTLSSecretRef},
			{Name: "custom", Number: 8443, ExposeToPublic: true, TLS: true},
		}
		resource.Status.Conditions = []metav1.Condition{{
			Type:               string(v1alpha1.StackResourceTLSConfigured),
			Status:             metav1.ConditionFalse,
			Reason:             reasonTLSWaiting,
			Message:            referencedTLSSecretRef,
			LastTransitionTime: metav1.NewTime(time.Now().Add(-tlsGracePeriod)),
		}}

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageWaiting))
		Expect(status.RetryAfter).To(BeNumerically(">", tlsGracePeriod-time.Second))
	})

	It("marks a missing source secret unavailable after the grace period", func() {
		newReconciler()
		resource.Status.ReferencedTLSSecret = &v1alpha1.ReferencedTLSSecretStatus{
			Reference:    referencedTLSSecretRef,
			WaitingSince: &metav1.Time{Time: time.Now().Add(-tlsGracePeriod)},
		}

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageUnavailable))
		Expect(status.Reason).To(Equal(reasonTLSSecretUnavailable))
		Expect(status.RetryAfter).To(BeZero())
	})

	It("restores a replica that was deleted out from under it", func() {
		newReconciler(sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first"))

		_, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconciler.Client.Delete(ctx, replicaIn(resource.Namespace))).To(Succeed())

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageReady))
		Expect(replicaIn(resource.Namespace).Data).To(HaveKeyWithValue(corev1.TLSCertKey, []byte("first")))
	})

	It("repairs a replica whose data was edited", func() {
		newReconciler(sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first"))

		_, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())

		tampered := replicaIn(resource.Namespace)
		tampered.Data[corev1.TLSCertKey] = []byte("garbage")
		Expect(reconciler.Client.Update(ctx, tampered)).To(Succeed())

		_, err = reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(replicaIn(resource.Namespace).Data).To(HaveKeyWithValue(corev1.TLSCertKey, []byte("first")))
	})

	It("waits while the source secret exists but carries no certificate yet", func() {
		empty := sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "")
		delete(empty.Data, corev1.TLSCertKey)
		newReconciler(empty)

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageWaiting))
		Expect(status.Reason).To(Equal(reasonTLSWaiting))

		By("nothing is replicated, so no port publishes https against a self-signed default")
		replica := &corev1.Secret{}
		key := types.NamespacedName{Namespace: resource.Namespace, Name: sourceSecretName}
		Expect(reconciler.Client.Get(ctx, key, replica)).NotTo(Succeed())
	})

	It("waits while the source secret has a certificate but no private key", func() {
		partial := sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "cert")
		delete(partial.Data, corev1.TLSPrivateKeyKey)
		newReconciler(partial)

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageWaiting))
		Expect(status.RetryAfter).To(BeNumerically(">", 0))

		replica := &corev1.Secret{}
		key := types.NamespacedName{Namespace: resource.Namespace, Name: sourceSecretName}
		Expect(reconciler.Client.Get(ctx, key, replica)).NotTo(Succeed())
	})

	It("replaces a replica of the wrong type that it put there itself", func() {
		ours := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      sourceSecretName,
				Namespace: resource.Namespace,
				Labels:    map[string]string{tlsSecretSourceLabel: sourceSecretNamespace},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{corev1.TLSCertKey: []byte("stale")},
		}
		newReconciler(sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first"), ours)

		By("the type is immutable, so this pass deletes it and stays pending")
		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageWaiting))
		Expect(status.Reason).To(Equal(reasonTLSWaiting))
		Expect(reconciler.Client.Get(ctx, types.NamespacedName{
			Namespace: resource.Namespace, Name: sourceSecretName,
		}, &corev1.Secret{})).NotTo(Succeed())

		By("the next pass recreates it with the right type")
		status, err = reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageReady))
		Expect(replicaIn(resource.Namespace).Type).To(Equal(corev1.SecretTypeTLS))
	})

	It("leaves a same-named secret of another type that is not ours alone", func() {
		theirs := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: sourceSecretName, Namespace: resource.Namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"config": []byte("not ours")},
		}
		newReconciler(sourceTLSSecret(sourceSecretNamespace, sourceSecretName, "first"), theirs)

		status, err := reconciler.reconcileReferencedTLSSecret(ctx, resource, referencedTLSSecretRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Stage).To(Equal(tlsStageWaiting))

		survivor := replicaIn(resource.Namespace)
		Expect(survivor.Type).To(Equal(corev1.SecretTypeOpaque))
		Expect(survivor.Data).To(HaveKeyWithValue("config", []byte("not ours")))
	})

})
