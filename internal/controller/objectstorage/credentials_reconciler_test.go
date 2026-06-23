package objectstorage

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/objectstorage/mocks"
)

func TestCredentialsReconciler_CreatesSecret(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	ctx := context.Background()

	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.CredentialsSecretName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, resource.CredentialsSecretName()))

	mockClient.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				t.Fatal("expected Secret object")
			}
			if secret.Name != resource.CredentialsSecretName() {
				t.Errorf("expected secret name %s, got %s", resource.CredentialsSecretName(), secret.Name)
			}
			if _, ok := secret.Data[storagev1alpha1.ObjectStorageSecretKeyAccessKey]; !ok {
				t.Error("missing RUSTFS_ACCESS_KEY")
			}
			if _, ok := secret.Data[storagev1alpha1.ObjectStorageSecretKeySecretKey]; !ok {
				t.Error("missing RUSTFS_SECRET_KEY")
			}
			if _, ok := secret.Data[storagev1alpha1.ObjectStorageSecretKeyAWSAccessKey]; !ok {
				t.Error("missing AWS_ACCESS_KEY_ID")
			}
			if _, ok := secret.Data[storagev1alpha1.ObjectStorageSecretKeyAWSSecretKey]; !ok {
				t.Error("missing AWS_SECRET_ACCESS_KEY")
			}
			if len(secret.Data[storagev1alpha1.ObjectStorageSecretKeyAccessKey]) < 16 {
				t.Error("access key too short")
			}
			return nil
		},
	)

	rec := newCredentialsReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultRequeue {
		t.Error("expected resultRequeue after creating Secret")
	}
}

func TestCredentialsReconciler_IdempotentOnExisting(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	ctx := context.Background()

	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.CredentialsSecretName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).DoAndReturn(
		func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			secret := obj.(*corev1.Secret)
			secret.Name = key.Name
			secret.Namespace = key.Namespace
			return nil
		},
	)

	rec := newCredentialsReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultNil {
		t.Error("expected resultNil when Secret already exists")
	}
	if resource.Status.CredentialsSecretName != resource.CredentialsSecretName() {
		t.Error("expected status.credentialsSecretName to be set")
	}
}
