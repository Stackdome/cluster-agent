package objectstorage

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/internal/controller/objectstorage/mocks"
)

func TestIngressReconciler_CreatesIngressWithTLS(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	resource.Spec.Ingress.TLS = true
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
			if ing.Annotations["cert-manager.io/cluster-issuer"] == "" {
				t.Error("expected cert-manager annotation when TLS is enabled")
			}
			if len(ing.Spec.TLS) != 1 {
				t.Fatal("expected 1 TLS entry")
			}
			if ing.Spec.TLS[0].Hosts[0] != "s3.example.com" {
				t.Errorf("expected TLS host s3.example.com, got %s", ing.Spec.TLS[0].Hosts[0])
			}
			if len(ing.Spec.Rules) != 1 {
				t.Fatal("expected 1 rule")
			}
			rule := ing.Spec.Rules[0]
			if rule.Host != "s3.example.com" {
				t.Errorf("expected host s3.example.com, got %s", rule.Host)
			}
			backend := rule.HTTP.Paths[0].Backend.Service
			if backend.Name != resource.ServiceName() {
				t.Errorf("expected service name %s, got %s", resource.ServiceName(), backend.Name)
			}
			if backend.Port.Number != 7480 {
				t.Errorf("expected port 7480, got %d", backend.Port.Number)
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

func TestIngressReconciler_SetsExternalEndpoint(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	scheme := testScheme()
	resource := testObjectStorage()
	resource.Spec.Ingress.TLS = true
	ctx := context.Background()

	mockClient.EXPECT().Get(gomock.Any(), client.ObjectKey{
		Name:      resource.IngressName(),
		Namespace: resource.Namespace,
	}, gomock.Any()).DoAndReturn(
		func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			ing := obj.(*networkingv1.Ingress)
			ing.Name = key.Name
			ing.Namespace = key.Namespace
			return nil
		},
	)

	rec := newIngressReconciler(mockClient, scheme)
	result, err := rec.reconcile(ctx, resource)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.resultNil {
		t.Error("expected resultNil when Ingress exists")
	}
	expected := "https://s3.example.com"
	if resource.Status.ExternalEndpoint != expected {
		t.Errorf("expected externalEndpoint %s, got %s", expected, resource.Status.ExternalEndpoint)
	}
}
