package objectstorage

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/internal/controller/objectstorage/mocks"
)

func TestDeploymentReconciler_CreatesDeployment(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	resource.Status.VolumeName = resource.VolumeName()
	resource.Status.CredentialsSecretName = resource.CredentialsSecretName()
	ctx := context.Background()

	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.DeploymentName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, resource.DeploymentName()))

	mockClient.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
			dep, ok := obj.(*appsv1.Deployment)
			if !ok {
				t.Fatal("expected Deployment object")
			}
			if dep.Name != resource.DeploymentName() {
				t.Errorf("expected deployment name %s, got %s", resource.DeploymentName(), dep.Name)
			}
			containers := dep.Spec.Template.Spec.Containers
			if len(containers) != 1 {
				t.Fatalf("expected 1 container, got %d", len(containers))
			}
			if containers[0].Image != "quay.io/s3gw/s3gw:latest" {
				t.Errorf("expected s3gw image, got %s", containers[0].Image)
			}
			if len(containers[0].Ports) == 0 || containers[0].Ports[0].ContainerPort != 7480 {
				t.Error("expected port 7480")
			}
			return nil
		},
	)

	rec := newDeploymentReconciler(mockClient, scheme, "quay.io/s3gw/s3gw:latest")
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultRequeue {
		t.Error("expected resultRequeue after creating Deployment")
	}
}
