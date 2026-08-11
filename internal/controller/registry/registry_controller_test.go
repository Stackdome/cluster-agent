package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/bcrypt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8sapierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/mocks"
	internaltypes "stackdome.io/cluster-agent/internal/types"
	reg "stackdome.io/cluster-agent/pkg/registry"
	"stackdome.io/cluster-agent/pkg/registry/zotregistry"
)

type failOnceClient struct {
	client.Client
	operation string
	key       client.ObjectKey
	failed    bool
}

func (c *failOnceClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if !c.failed && c.operation == "get" && key == c.key {
		if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
			c.failed = true
			return errors.New("transient get failure")
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *failOnceClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if !c.failed && c.operation == "delete" && client.ObjectKeyFromObject(obj) == c.key {
		if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
			c.failed = true
			return errors.New("transient delete failure")
		}
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func deletingRegistry(name string) *registryv1alpha1.ClusterRegistry {
	now := metav1.Now()
	return &registryv1alpha1.ClusterRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Finalizers:        []string{cacheFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: registryv1alpha1.ClusterRegistrySpec{
			Storage: registryv1alpha1.RegistryStorageSpec{Size: "10Gi"},
			Port:    5000,
		},
	}
}

func deletionTestReconciler(t *testing.T, objects ...client.Object) (*RegistryReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	if err := registryv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add registry scheme: %v", err)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	reconciler := NewRegistryReconciler(k8sClient, scheme, zotregistry.NewZotRegistry(zotregistry.ZotRegistryOpts{}))
	return reconciler, k8sClient
}

func assertRegistryFinalizerPresent(t *testing.T, k8sClient client.Client, name string) {
	t.Helper()
	registry := &registryv1alpha1.ClusterRegistry{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: name}, registry); err != nil {
		t.Fatalf("get ClusterRegistry: %v", err)
	}
	if !containsString(registry.Finalizers, cacheFinalizer) {
		t.Fatalf("finalizer %q was removed while owned resources remain", cacheFinalizer)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRegistryDeletionRetainsFinalizerUntilExactResourcesAreAbsent(t *testing.T) {
	ctx := context.Background()
	registry := deletingRegistry("test-reg")
	statefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "test-reg", Namespace: registryNamespace}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "storage-test-reg-0", Namespace: registryNamespace}}
	unrelatedPVC := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "storage-other-reg-0", Namespace: registryNamespace}}
	reconciler, k8sClient := deletionTestReconciler(t, registry, statefulSet, pvc, unrelatedPVC)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: registry.Name}}

	for step, deleted := range []client.Object{statefulSet, pvc} {
		result, err := reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("reconcile step %d returned an error: %v", step+1, err)
		}
		if result.RequeueAfter <= 0 {
			t.Fatalf("reconcile step %d did not requeue while resources remained: %#v", step+1, result)
		}
		assertRegistryFinalizerPresent(t, k8sClient, registry.Name)
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(deleted), deleted); !k8sapierrors.IsNotFound(err) {
			t.Fatalf("resource %T %s was not deleted exactly: %v", deleted, deleted.GetName(), err)
		}
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unrelatedPVC), &corev1.PersistentVolumeClaim{}); err != nil {
			t.Fatalf("unrelated PVC was deleted at step %d: %v", step+1, err)
		}
	}

	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("final reconcile returned an error: %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("final reconcile result = %#v, want empty", result)
	}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: registry.Name}, &registryv1alpha1.ClusterRegistry{}); !k8sapierrors.IsNotFound(err) {
		t.Fatalf("ClusterRegistry still exists after finalizer removal: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unrelatedPVC), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("unrelated PVC was deleted: %v", err)
	}
}

func TestRegistryDeletionRequeuesWhileStatefulSetIsTerminating(t *testing.T) {
	ctx := context.Background()
	registry := deletingRegistry("test-reg")
	now := metav1.Now()
	statefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
		Name:              "test-reg",
		Namespace:         registryNamespace,
		DeletionTimestamp: &now,
		Finalizers:        []string{"example.com/hold"},
	}}
	reconciler, k8sClient := deletionTestReconciler(t, registry, statefulSet)

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: registry.Name}})
	if err != nil {
		t.Fatalf("Reconcile returned an error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("Reconcile result = %#v, want delayed requeue", result)
	}
	assertRegistryFinalizerPresent(t, k8sClient, registry.Name)
}

func TestRegistryDeletionRetriesTransientPVCErrors(t *testing.T) {
	for _, operation := range []string{"get", "delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			registry := deletingRegistry("test-reg")
			pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "storage-test-reg-0", Namespace: registryNamespace}}
			reconciler, baseClient := deletionTestReconciler(t, registry, pvc)
			failingClient := &failOnceClient{
				Client:    baseClient,
				operation: operation,
				key:       client.ObjectKeyFromObject(pvc),
			}
			reconciler.Client = failingClient
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: registry.Name}}

			if _, err := reconciler.Reconcile(ctx, req); err == nil || !strings.Contains(err.Error(), "transient "+operation+" failure") {
				t.Fatalf("first Reconcile error = %v, want transient %s failure", err, operation)
			}
			assertRegistryFinalizerPresent(t, baseClient, registry.Name)

			result, err := reconciler.Reconcile(ctx, req)
			if err != nil {
				t.Fatalf("retry Reconcile returned an error: %v", err)
			}
			if result.RequeueAfter <= 0 {
				t.Fatalf("retry Reconcile result = %#v, want delayed requeue", result)
			}
			assertRegistryFinalizerPresent(t, baseClient, registry.Name)
			if err := baseClient.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); !k8sapierrors.IsNotFound(err) {
				t.Fatalf("PVC still exists after successful retry: %v", err)
			}

			if _, err := reconciler.Reconcile(ctx, req); err != nil {
				t.Fatalf("final Reconcile returned an error: %v", err)
			}
			if err := baseClient.Get(ctx, client.ObjectKey{Name: registry.Name}, &registryv1alpha1.ClusterRegistry{}); !k8sapierrors.IsNotFound(err) {
				t.Fatalf("ClusterRegistry still exists after cleanup completed: %v", err)
			}
		})
	}
}

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

var _ = Describe("detectContainerRuntime", func() {
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

	It("returns RuntimeContainerd for standard kubelet version", func() {
		mockClient.EXPECT().
			List(ctx, gomock.AssignableToTypeOf(&corev1.NodeList{}), gomock.Any()).
			DoAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				nodeList := list.(*corev1.NodeList)
				nodeList.Items = []corev1.Node{
					{
						Status: corev1.NodeStatus{
							NodeInfo: corev1.NodeSystemInfo{
								KubeletVersion: "v1.31.5",
							},
						},
					},
				}
				return nil
			})

		runtime, err := reconciler.detectContainerRuntime(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(runtime).To(Equal(reg.RuntimeContainerd))
	})

	It("returns RuntimeK3s for k3s kubelet version", func() {
		mockClient.EXPECT().
			List(ctx, gomock.AssignableToTypeOf(&corev1.NodeList{}), gomock.Any()).
			DoAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				nodeList := list.(*corev1.NodeList)
				nodeList.Items = []corev1.Node{
					{
						Status: corev1.NodeStatus{
							NodeInfo: corev1.NodeSystemInfo{
								KubeletVersion: "v1.31.5+k3s1",
							},
						},
					},
				}
				return nil
			})

		runtime, err := reconciler.detectContainerRuntime(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(runtime).To(Equal(reg.RuntimeK3s))
	})

	It("returns RuntimeContainerd when no nodes exist", func() {
		mockClient.EXPECT().
			List(ctx, gomock.AssignableToTypeOf(&corev1.NodeList{}), gomock.Any()).
			DoAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				nodeList := list.(*corev1.NodeList)
				nodeList.Items = []corev1.Node{}
				return nil
			})

		runtime, err := reconciler.detectContainerRuntime(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(runtime).To(Equal(reg.RuntimeContainerd))
	})

	It("returns RuntimeContainerd with error when List fails", func() {
		mockClient.EXPECT().
			List(ctx, gomock.AssignableToTypeOf(&corev1.NodeList{}), gomock.Any()).
			Return(fmt.Errorf("connection refused"))

		runtime, err := reconciler.detectContainerRuntime(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to list nodes"))
		Expect(runtime).To(Equal(reg.RuntimeContainerd))
	})
})
