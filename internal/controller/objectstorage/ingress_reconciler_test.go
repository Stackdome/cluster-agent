package objectstorage

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/internal/controller/objectstorage/mocks"
)

func TestIngressReconciler_CreatesIngressWithoutTLS(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	resource.Spec.Ingress.TLS = false
	ctx := context.Background()

	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.IngressName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).Return(apierrors.NewNotFound(schema.GroupResource{}, resource.IngressName()))

	mockClient.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
			ing, ok := obj.(*networkingv1.Ingress)
			if !ok {
				t.Fatal("expected Ingress object")
			}
			if len(ing.Spec.TLS) != 0 {
				t.Error("expected no TLS when TLS is disabled")
			}
			if len(ing.Spec.Rules) != 1 {
				t.Fatal("expected 1 rule")
			}
			if ing.Spec.Rules[0].Host != "s3.example.com" {
				t.Errorf("expected host s3.example.com, got %s", ing.Spec.Rules[0].Host)
			}
			backend := ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service
			if backend.Name != resource.ServiceName() {
				t.Errorf("expected service name %s, got %s", resource.ServiceName(), backend.Name)
			}
			if backend.Port.Number != storagev1alpha1.ObjectStorageContainerPort {
				t.Errorf("expected port %d, got %d", storagev1alpha1.ObjectStorageContainerPort, backend.Port.Number)
			}
			return nil
		},
	)

	rec := newIngressReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultRequeue {
		t.Error("expected resultRequeue after creating Ingress")
	}
}

func TestIngressReconciler_SetsExternalEndpointWhenIngressExists(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	resource.Spec.Ingress.TLS = false
	ctx := context.Background()

	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.IngressName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).DoAndReturn(
		func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			ing := obj.(*networkingv1.Ingress)
			ing.Name = key.Name
			ing.Namespace = key.Namespace
			ing.Annotations = map[string]string{}
			ing.Spec = networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{
					{
						Host: "s3.example.com",
						IngressRuleValue: networkingv1.IngressRuleValue{
							HTTP: &networkingv1.HTTPIngressRuleValue{
								Paths: []networkingv1.HTTPIngressPath{
									{
										Path:     "/",
										PathType: ptr.To(networkingv1.PathTypePrefix),
										Backend: networkingv1.IngressBackend{
											Service: &networkingv1.IngressServiceBackend{
												Name: resource.ServiceName(),
												Port: networkingv1.ServiceBackendPort{Number: storagev1alpha1.ObjectStorageContainerPort},
											},
										},
									},
								},
							},
						},
					},
				},
			}
			return nil
		},
	)

	rec := newIngressReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultNil {
		t.Error("expected resultNil when Ingress already exists and matches")
	}
	expected := "http://s3.example.com"
	if resource.Status.ExternalEndpoint != expected {
		t.Errorf("expected externalEndpoint %s, got %s", expected, resource.Status.ExternalEndpoint)
	}
}

func TestIngressReconciler_SkipsWhenIngressNil(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	resource.Spec.Ingress = nil
	ctx := context.Background()

	rec := newIngressReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultNil {
		t.Error("expected resultNil when Ingress is nil")
	}
}
