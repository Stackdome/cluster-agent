package helpers

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
)

func WaitForClusterRegistryReady(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*registryv1alpha1.ClusterRegistry, error) {
	return WaitFor(ctx, c, key, &registryv1alpha1.ClusterRegistry{}, func(reg *registryv1alpha1.ClusterRegistry) bool {
		return reg.Status.Phase == registryv1alpha1.RegistryPhaseRunning
	}, timeout)
}

func WaitForClusterRegistryDeleted(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) error {
	return WaitForDeleted(ctx, c, key, &registryv1alpha1.ClusterRegistry{}, timeout)
}

func ClusterRegistryHasCondition(reg *registryv1alpha1.ClusterRegistry, conditionType string, status metav1.ConditionStatus) bool {
	cond := meta.FindStatusCondition(reg.Status.Conditions, conditionType)
	return cond != nil && cond.Status == status
}
