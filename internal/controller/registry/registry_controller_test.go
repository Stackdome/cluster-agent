package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/bcrypt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8sapierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/mocks"
	internaltypes "stackdome.io/cluster-agent/internal/types"
	reg "stackdome.io/cluster-agent/pkg/registry"
)

func TestRegistryController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Registry Controller Suite")
}

var _ = Describe("htpasswdCredentialsMatch", func() {
	var (
		username string
		password string
		hash     []byte
	)

	BeforeEach(func() {
		username = "admin"
		password = "secret123"
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		Expect(err).NotTo(HaveOccurred())
		hash = h
	})

	It("returns true for matching credentials", func() {
		stored := []byte(fmt.Sprintf("%s:%s", username, string(hash)))
		Expect(htpasswdCredentialsMatch(stored, username, password)).To(BeTrue())
	})

	It("returns false for username mismatch", func() {
		stored := []byte(fmt.Sprintf("%s:%s", username, string(hash)))
		Expect(htpasswdCredentialsMatch(stored, "other-user", password)).To(BeFalse())
	})

	It("returns false for password mismatch", func() {
		stored := []byte(fmt.Sprintf("%s:%s", username, string(hash)))
		Expect(htpasswdCredentialsMatch(stored, username, "wrong-password")).To(BeFalse())
	})

	It("returns false for empty stored entry", func() {
		Expect(htpasswdCredentialsMatch([]byte{}, username, password)).To(BeFalse())
	})

	It("returns false for malformed entry without colon", func() {
		Expect(htpasswdCredentialsMatch([]byte("nocolonhere"), username, password)).To(BeFalse())
	})

	It("handles entry with colon in bcrypt hash", func() {
		stored := []byte(fmt.Sprintf("%s:%s", username, string(hash)))
		Expect(htpasswdCredentialsMatch(stored, username, password)).To(BeTrue())
	})
})

var _ = Describe("cleanupSharedRegistryConfig", func() {
	var (
		mockCtrl   *gomock.Controller
		mockClient *mocks.MockClient
		reconciler *RegistryReconciler
		ctx        context.Context
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mocks.NewMockClient(mockCtrl)
		ctx = context.Background()
		reconciler = &RegistryReconciler{
			Client: mockClient,
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	It("skips cleanup when InternalURL is empty", func() {
		registry := &registryv1alpha1.ClusterRegistry{
			Status: registryv1alpha1.RegistryStatus{
				InternalURL: "",
			},
		}
		err := reconciler.cleanupSharedRegistryConfig(ctx, registry)
		Expect(err).NotTo(HaveOccurred())
	})

	It("handles ConfigMap not found gracefully", func() {
		registry := &registryv1alpha1.ClusterRegistry{
			Status: registryv1alpha1.RegistryStatus{
				InternalURL: "http://test-registry.stackdome-registry.svc.cluster.local",
			},
		}

		mockClient.EXPECT().
			Get(ctx, types.NamespacedName{Name: nodeRegistryAccessConfigMapName, Namespace: registryNamespace}, gomock.AssignableToTypeOf(&corev1.ConfigMap{})).
			Return(k8sapierrors.NewNotFound(schema.GroupResource{}, nodeRegistryAccessConfigMapName))

		err := reconciler.cleanupSharedRegistryConfig(ctx, registry)
		Expect(err).NotTo(HaveOccurred())
	})

	It("deletes ConfigMap and DaemonSet when last registry is removed", func() {
		registryURL := "http://test-registry.stackdome-registry.svc.cluster.local"
		registry := &registryv1alpha1.ClusterRegistry{
			Status: registryv1alpha1.RegistryStatus{
				InternalURL: registryURL,
				ServiceIP:   "10.96.0.100",
			},
		}

		registryConfig := internaltypes.NewRegistryConfig()
		registryConfig.AddRegistry("10.96.0.100", registryURL)
		configJSON, err := json.Marshal(registryConfig)
		Expect(err).NotTo(HaveOccurred())

		mockClient.EXPECT().
			Get(ctx, types.NamespacedName{Name: nodeRegistryAccessConfigMapName, Namespace: registryNamespace}, gomock.AssignableToTypeOf(&corev1.ConfigMap{})).
			DoAndReturn(func(_ context.Context, _ types.NamespacedName, cm *corev1.ConfigMap, _ ...client.GetOption) error {
				cm.Name = nodeRegistryAccessConfigMapName
				cm.Namespace = registryNamespace
				cm.Data = map[string]string{"registries.json": string(configJSON)}
				return nil
			})

		mockClient.EXPECT().
			Delete(ctx, gomock.AssignableToTypeOf(&corev1.ConfigMap{})).
			Return(nil)

		mockClient.EXPECT().
			Delete(ctx, gomock.AssignableToTypeOf(&appsv1.DaemonSet{})).
			DoAndReturn(func(_ context.Context, ds client.Object, _ ...client.DeleteOption) error {
				Expect(ds.GetName()).To(Equal(reg.RegistryConfigReconcilerDaemonSetName))
				Expect(ds.GetNamespace()).To(Equal(registryNamespace))
				return nil
			})

		err = reconciler.cleanupSharedRegistryConfig(ctx, registry)
		Expect(err).NotTo(HaveOccurred())
	})

	It("only removes entry and updates ConfigMap when other registries remain", func() {
		registryURL := "http://test-registry.stackdome-registry.svc.cluster.local"
		otherURL := "http://other-registry.stackdome-registry.svc.cluster.local"
		registry := &registryv1alpha1.ClusterRegistry{
			Status: registryv1alpha1.RegistryStatus{
				InternalURL: registryURL,
				ServiceIP:   "10.96.0.100",
			},
		}

		registryConfig := internaltypes.NewRegistryConfig()
		registryConfig.AddRegistry("10.96.0.100", registryURL)
		registryConfig.AddRegistry("10.96.0.200", otherURL)
		configJSON, err := json.Marshal(registryConfig)
		Expect(err).NotTo(HaveOccurred())

		mockClient.EXPECT().
			Get(ctx, types.NamespacedName{Name: nodeRegistryAccessConfigMapName, Namespace: registryNamespace}, gomock.AssignableToTypeOf(&corev1.ConfigMap{})).
			DoAndReturn(func(_ context.Context, _ types.NamespacedName, cm *corev1.ConfigMap, _ ...client.GetOption) error {
				cm.Name = nodeRegistryAccessConfigMapName
				cm.Namespace = registryNamespace
				cm.Data = map[string]string{"registries.json": string(configJSON)}
				return nil
			})

		mockClient.EXPECT().
			Update(ctx, gomock.AssignableToTypeOf(&corev1.ConfigMap{})).
			DoAndReturn(func(_ context.Context, cm client.Object, _ ...client.UpdateOption) error {
				configMap := cm.(*corev1.ConfigMap)
				var updatedConfig internaltypes.RegistryConfig
				Expect(json.Unmarshal([]byte(configMap.Data["registries.json"]), &updatedConfig)).To(Succeed())
				Expect(updatedConfig.HasEndpoint(otherURL)).To(BeTrue())
				Expect(updatedConfig.HasEndpoint(registryURL)).To(BeFalse())
				return nil
			})

		err = reconciler.cleanupSharedRegistryConfig(ctx, registry)
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("reconcileRegistryNamespace", func() {
	var (
		mockCtrl   *gomock.Controller
		mockClient *mocks.MockClient
		reconciler *RegistryReconciler
		ctx        context.Context
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mocks.NewMockClient(mockCtrl)
		ctx = context.Background()
		reconciler = &RegistryReconciler{
			Client: mockClient,
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	It("creates namespace without spec finalizers", func() {
		registry := &registryv1alpha1.ClusterRegistry{}

		mockClient.EXPECT().
			Get(ctx, types.NamespacedName{Name: registryNamespace}, gomock.AssignableToTypeOf(&corev1.Namespace{})).
			Return(k8sapierrors.NewNotFound(schema.GroupResource{}, registryNamespace))

		mockClient.EXPECT().
			Create(ctx, gomock.AssignableToTypeOf(&corev1.Namespace{})).
			DoAndReturn(func(_ context.Context, ns client.Object, _ ...client.CreateOption) error {
				namespace := ns.(*corev1.Namespace)
				Expect(namespace.Name).To(Equal(registryNamespace))
				Expect(namespace.Spec.Finalizers).To(BeEmpty())
				return nil
			})

		result, err := reconciler.reconcileRegistryNamespace(ctx, registry)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.resultRequeue).To(BeTrue())
	})
})
