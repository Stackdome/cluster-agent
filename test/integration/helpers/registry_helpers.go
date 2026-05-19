package helpers

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
)

func WaitForClusterRegistryReady(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*registryv1alpha1.ClusterRegistry, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			reg := &registryv1alpha1.ClusterRegistry{}
			_ = c.Get(ctx, key, reg)
			return reg, fmt.Errorf("timed out waiting for ClusterRegistry %s to become Running (current phase: %s)", key.Name, reg.Status.Phase)
		case <-tick.C:
			reg := &registryv1alpha1.ClusterRegistry{}
			if err := c.Get(ctx, key, reg); err != nil {
				continue
			}
			if reg.Status.Phase == registryv1alpha1.RegistryPhaseRunning {
				return reg, nil
			}
		}
	}
}

func WaitForClusterRegistryDeleted(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) error {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timed out waiting for ClusterRegistry %s to be deleted", key.Name)
		case <-tick.C:
			reg := &registryv1alpha1.ClusterRegistry{}
			if err := c.Get(ctx, key, reg); err != nil {
				if errors.IsNotFound(err) {
					return nil
				}
			}
		}
	}
}

func ClusterRegistryHasCondition(reg *registryv1alpha1.ClusterRegistry, conditionType string, status metav1.ConditionStatus) bool {
	cond := meta.FindStatusCondition(reg.Status.Conditions, conditionType)
	return cond != nil && cond.Status == status
}
