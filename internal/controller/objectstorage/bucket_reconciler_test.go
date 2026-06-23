package objectstorage

import (
	"context"
	"fmt"
	"testing"

	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller/objectstorage/mocks"
)

func TestBucketReconciler_CreatesBuckets(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	mockS3 := mocks.NewMockS3BucketCreator(ctrl)
	resource := testObjectStorage()
	resource.Spec.Buckets = []storagev1alpha1.BucketSpec{
		{Name: "uploads"},
		{Name: "assets"},
	}
	resource.Status.Endpoint = fmt.Sprintf("http://test-os-objstore.default.svc.cluster.local:%d", storagev1alpha1.ObjectStorageContainerPort)
	resource.Status.CredentialsSecretName = resource.CredentialsSecretName()
	ctx := context.Background()

	mockS3.EXPECT().CreateBucket(gomock.Any(), "uploads").Return(nil)
	mockS3.EXPECT().CreateBucket(gomock.Any(), "assets").Return(nil)

	rec := &bucketReconciler{client: mockClient, bucketCreator: mockS3}
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultNil {
		t.Error("expected resultNil after creating buckets")
	}
	if len(resource.Status.Buckets) != 2 {
		t.Fatalf("expected 2 bucket statuses, got %d", len(resource.Status.Buckets))
	}
	for _, bs := range resource.Status.Buckets {
		if !bs.Created {
			t.Errorf("expected bucket %s to be created", bs.Name)
		}
	}
	cond := meta.FindStatusCondition(resource.Status.Conditions, storagev1alpha1.ObjectStorageConditionBucketsReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected BucketsReady condition to be True")
	}
}

func TestBucketReconciler_NoBucketsInSpec(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	resource := testObjectStorage()
	resource.Spec.Buckets = nil
	ctx := context.Background()

	rec := &bucketReconciler{client: mockClient}
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultNil {
		t.Error("expected resultNil when no buckets in spec")
	}
}
