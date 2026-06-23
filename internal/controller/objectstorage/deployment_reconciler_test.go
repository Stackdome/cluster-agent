package objectstorage

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/pkg/config"
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
	resource.Status.PVCName = resource.VolumeName()
	resource.Status.CredentialsSecretName = resource.CredentialsSecretName()
	ctx := context.Background()

	// CreateOrUpdate does Get first — not found triggers Create
	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.DeploymentName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, resource.DeploymentName()))

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
			if containers[0].Image != config.RustFSImage {
				t.Errorf("expected rustfs image, got %s", containers[0].Image)
			}
			if len(containers[0].Ports) == 0 || containers[0].Ports[0].ContainerPort != storagev1alpha1.ObjectStorageContainerPort {
				t.Error("expected port matching ObjectStorageContainerPort")
			}
			return nil
		},
	)

	rec := newDeploymentReconciler(mockClient, scheme, config.RustFSImage)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After CreateOrUpdate creates a new deployment, it won't be available yet
	if !result.resultStop {
		t.Error("expected resultStop when newly created Deployment is not yet available")
	}
}
