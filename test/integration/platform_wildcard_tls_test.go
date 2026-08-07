package integration

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

var _ = Describe("Platform wildcard TLS", func() {
	It("falls back to HTTP when the source Secret is missing and recovers HTTPS when it appears", func() {
		const (
			controlPlaneNamespace = "stackdome-control-plane"
			secretName            = "integration-platform-wildcard-tls"
			stackName             = "platform-wildcard-tls-stack"
			resourceName          = "platform-wildcard-tls-app"
			fqdn                  = "platform-wildcard-tls-app.example.com"
		)

		resource := fixtures.NewResource(stackName, resourceName, fixtures.WithPorts(corev1alpha1.Port{
			Name: "http", Number: 80, Protocol: "http", ExposeToPublic: true,
			FQDN: fqdn, TLS: true, TLSSecretRef: secretName,
		}))
		resource.Labels[corev1alpha1.LabelUsesPlatformWildcardTLS] = "true"
		stackWithResources := fixtures.NewStack(stackName, resource)
		stack := stackWithResources.Stack
		resourceKey := client.ObjectKeyFromObject(resource)

		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: controlPlaneNamespace,
				Labels: map[string]string{
					corev1alpha1.LabelPlatformWildcardTLSSecret: "true",
				},
			},
			Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{
				corev1.TLSCertKey:       []byte("integration-certificate"),
				corev1.TLSPrivateKeyKey: []byte("integration-private-key"),
			},
		}
		replicaKey := client.ObjectKey{Name: secretName, Namespace: resource.Namespace}

		DeferCleanup(func() {
			helpers.CleanupStack(ctx, c, stack)
			_ = client.IgnoreNotFound(c.Delete(ctx, sourceSecret))
			_ = client.IgnoreNotFound(c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name: replicaKey.Name, Namespace: replicaKey.Namespace,
			}}))
		})

		Expect(fixtures.CreateStackWithResources(ctx, c, stackWithResources)).To(Succeed())

		By("waiting without publishing a URL while the source Secret is missing")
		waitingResource, err := helpers.WaitFor(ctx, c, resourceKey, &corev1alpha1.StackResource{}, func(resource *corev1alpha1.StackResource) bool {
			tlsConfigured := meta.FindStatusCondition(resource.Status.Conditions, string(corev1alpha1.StackResourceTLSConfigured))
			converged := meta.FindStatusCondition(resource.Status.Conditions, string(corev1alpha1.StackResourceConverged))
			return resource.Status.ReferencedTLSSecret != nil &&
				resource.Status.ReferencedTLSSecret.Reference == secretName &&
				resource.Status.ReferencedTLSSecret.WaitingSince != nil &&
				tlsConfigured != nil &&
				tlsConfigured.Status == metav1.ConditionFalse &&
				tlsConfigured.Reason == "CertificateIssuing" &&
				converged != nil &&
				converged.Status == metav1.ConditionFalse &&
				len(resource.Status.ExternalAddress) == 0
		}, readyTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("expiring the grace period and falling back to HTTP")
		waitingResource.Status.ReferencedTLSSecret.WaitingSince = &metav1.Time{
			Time: time.Now().Add(-2*time.Minute - time.Second),
		}
		Expect(c.Status().Update(ctx, waitingResource)).To(Succeed())

		fallbackResource, err := helpers.WaitFor(ctx, c, resourceKey, &corev1alpha1.StackResource{}, func(resource *corev1alpha1.StackResource) bool {
			tlsConfigured := meta.FindStatusCondition(resource.Status.Conditions, string(corev1alpha1.StackResourceTLSConfigured))
			available := meta.FindStatusCondition(resource.Status.Conditions, string(corev1alpha1.StackResourceStatusAvailable))
			converged := meta.FindStatusCondition(resource.Status.Conditions, string(corev1alpha1.StackResourceConverged))
			return tlsConfigured != nil &&
				tlsConfigured.Status == metav1.ConditionFalse &&
				tlsConfigured.Reason == "TLSSecretUnavailable" &&
				available != nil &&
				available.Status == metav1.ConditionTrue &&
				converged != nil &&
				converged.Status == metav1.ConditionTrue &&
				len(resource.Status.ExternalAddress) == 1 &&
				resource.Status.ExternalAddress[0].Address == "http://"+fqdn
		}, readyTimeout)
		Expect(err).NotTo(HaveOccurred())
		Expect(fallbackResource.Status.ExternalAddress[0].TargetPort).To(Equal(int32(80)))
		_, err = helpers.WaitForStackConverged(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("creating only the source Secret and recovering HTTPS through its watch")
		Expect(c.Create(ctx, sourceSecret)).To(Succeed())

		readyResource, err := helpers.WaitFor(ctx, c, resourceKey, &corev1alpha1.StackResource{}, func(resource *corev1alpha1.StackResource) bool {
			tlsConfigured := meta.FindStatusCondition(resource.Status.Conditions, string(corev1alpha1.StackResourceTLSConfigured))
			return resource.Status.ReferencedTLSSecret != nil &&
				resource.Status.ReferencedTLSSecret.Reference == secretName &&
				resource.Status.ReferencedTLSSecret.WaitingSince == nil &&
				tlsConfigured != nil &&
				tlsConfigured.Status == metav1.ConditionTrue &&
				tlsConfigured.Reason == "TLSReady" &&
				len(resource.Status.ExternalAddress) == 1 &&
				resource.Status.ExternalAddress[0].Address == "https://"+fqdn
		}, readyTimeout)
		Expect(err).NotTo(HaveOccurred())
		Expect(readyResource.Status.ExternalAddress[0].TargetPort).To(Equal(int32(80)))

		replica, err := helpers.WaitFor(ctx, c, replicaKey, &corev1.Secret{}, func(secret *corev1.Secret) bool {
			return secret.Type == corev1.SecretTypeTLS &&
				string(secret.Data[corev1.TLSCertKey]) == "integration-certificate" &&
				string(secret.Data[corev1.TLSPrivateKeyKey]) == "integration-private-key"
		}, fieldChangeTimeout)
		Expect(err).NotTo(HaveOccurred())
		Expect(replica.Labels).To(HaveKeyWithValue("core.stackdome.io/tls-secret-source", controlPlaneNamespace))
	})
})
