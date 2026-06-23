package objectstorage

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/objectstorage/mocks"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = storagev1alpha1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = networkingv1.AddToScheme(s)
	return s
}

func testObjectStorage() *storagev1alpha1.ObjectStorage {
	return &storagev1alpha1.ObjectStorage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-os",
			Namespace: "default",
			UID:       "test-uid-123",
		},
		Spec: storagev1alpha1.ObjectStorageSpec{
			Capacity: "10Gi",
			Ingress: storagev1alpha1.ObjectStorageIngressSpec{
				Hostname: "s3.example.com",
				TLS:      true,
			},
		},
	}
}

func TestVolumeReconciler_CreatesVolume(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	mockStatusClient := mocks.NewMockStatusClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	ctx := context.Background()

	// Volume does not exist yet
	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.VolumeName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, resource.VolumeName()))

	// Expect Create to be called with the Volume
	mockClient.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
			vol, ok := obj.(*storagev1alpha1.Volume)
			if !ok {
				t.Fatal("expected Volume object")
			}
			if vol.Name != resource.VolumeName() {
				t.Errorf("expected volume name %s, got %s", resource.VolumeName(), vol.Name)
			}
			if vol.Spec.Size != "10Gi" {
				t.Errorf("expected size 10Gi, got %s", vol.Spec.Size)
			}
			if vol.Spec.AccessMode != corev1.ReadWriteOnce {
				t.Errorf("expected RWO access mode")
			}
			return nil
		},
	)

	_ = mockStatusClient // unused in this test

	rec := newVolumeReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultRequeue {
		t.Error("expected resultRequeue after creating Volume")
	}
}

func TestVolumeReconciler_StopsWhenVolumeNotReady(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	ctx := context.Background()

	// Volume exists but not ready
	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.VolumeName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).DoAndReturn(
		func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			vol := obj.(*storagev1alpha1.Volume)
			vol.Name = key.Name
			vol.Namespace = key.Namespace
			vol.Status.Phase = storagev1alpha1.VolumePhasePending
			return nil
		},
	)

	rec := newVolumeReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultStop {
		t.Error("expected resultStop when Volume is not ready")
	}
}

func TestVolumeReconciler_ContinuesWhenVolumeReady(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	ctx := context.Background()

	// Volume exists and ready
	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.VolumeName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).DoAndReturn(
		func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			vol := obj.(*storagev1alpha1.Volume)
			vol.Name = key.Name
			vol.Namespace = key.Namespace
			vol.Status.Phase = storagev1alpha1.VolumePhaseReady
			return nil
		},
	)

	rec := newVolumeReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultNil {
		t.Error("expected resultNil when Volume is ready")
	}
	if resource.Status.VolumeName != resource.VolumeName() {
		t.Errorf("expected status.volumeName to be set")
	}
}
