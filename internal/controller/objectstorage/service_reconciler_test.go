package objectstorage

import (
	"context"
	"testing"

	"fmt"

	"go.uber.org/mock/gomock"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/internal/controller/objectstorage/mocks"
)

func TestServiceReconciler_CreatesService(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	ctx := context.Background()

	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.ServiceName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, resource.ServiceName()))

	mockClient.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
			svc, ok := obj.(*corev1.Service)
			if !ok {
				t.Fatal("expected Service object")
			}
			if svc.Name != resource.ServiceName() {
				t.Errorf("expected service name %s, got %s", resource.ServiceName(), svc.Name)
			}
			if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != storagev1alpha1.ObjectStorageContainerPort {
				t.Error("expected port matching ObjectStorageContainerPort")
			}
			if svc.Spec.Selector["app"] != resource.DeploymentName() {
				t.Errorf("expected selector app=%s", resource.DeploymentName())
			}
			return nil
		},
	)

	rec := newServiceReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultRequeue {
		t.Error("expected resultRequeue after creating Service")
	}
}

func TestServiceReconciler_SetsEndpointWhenExists(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	ctx := context.Background()

	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.ServiceName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).DoAndReturn(
		func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			svc := obj.(*corev1.Service)
			svc.Name = key.Name
			svc.Namespace = key.Namespace
			svc.Spec.ClusterIP = "10.96.0.100"
			return nil
		},
	)

	rec := newServiceReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultNil {
		t.Error("expected resultNil when Service already exists")
	}
	expected := fmt.Sprintf("http://test-os-objstore.default.svc.cluster.local:%d", storagev1alpha1.ObjectStorageContainerPort)
	if resource.Status.Endpoint != expected {
		t.Errorf("expected endpoint %s, got %s", expected, resource.Status.Endpoint)
	}
}
