package helpers

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
)

// WaitForPostgresClusterReady polls until the PostgresCluster reaches ClusterReady=True
// or the timeout expires.
func WaitForPostgresClusterReady(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*addonsv1alpha1.PostgresCluster, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			pg := &addonsv1alpha1.PostgresCluster{}
			_ = c.Get(ctx, key, pg)
			return pg, fmt.Errorf("timed out waiting for PostgresCluster %s to become Ready (current phase: %s)", key.Name, pg.Status.Phase)
		case <-tick.C:
			pg := &addonsv1alpha1.PostgresCluster{}
			if err := c.Get(ctx, key, pg); err != nil {
				continue
			}
			cond := meta.FindStatusCondition(pg.Status.Conditions, string(addonsv1alpha1.ClusterReady))
			if cond != nil && cond.Status == metav1.ConditionTrue {
				return pg, nil
			}
		}
	}
}

// WaitForCondition polls until a specific condition is set on the PostgresCluster.
func WaitForCondition(ctx context.Context, c client.Client, key client.ObjectKey, conditionType addonsv1alpha1.PostgresClusterConditionType, expectedStatus metav1.ConditionStatus, timeout time.Duration) (*addonsv1alpha1.PostgresCluster, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			pg := &addonsv1alpha1.PostgresCluster{}
			_ = c.Get(ctx, key, pg)
			return pg, fmt.Errorf("timed out waiting for condition %s=%s on PostgresCluster %s", conditionType, expectedStatus, key.Name)
		case <-tick.C:
			pg := &addonsv1alpha1.PostgresCluster{}
			if err := c.Get(ctx, key, pg); err != nil {
				continue
			}
			cond := meta.FindStatusCondition(pg.Status.Conditions, string(conditionType))
			if cond != nil && cond.Status == expectedStatus {
				return pg, nil
			}
		}
	}
}

// WaitForPostgresClusterDeleted polls until the PostgresCluster no longer exists.
func WaitForPostgresClusterDeleted(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) error {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timed out waiting for PostgresCluster %s to be deleted", key.Name)
		case <-tick.C:
			pg := &addonsv1alpha1.PostgresCluster{}
			err := c.Get(ctx, key, pg)
			if err != nil {
				return nil
			}
		}
	}
}

// HasCondition checks if a PostgresCluster has a specific condition with the expected status.
func HasCondition(pg *addonsv1alpha1.PostgresCluster, conditionType addonsv1alpha1.PostgresClusterConditionType, status metav1.ConditionStatus) bool {
	cond := meta.FindStatusCondition(pg.Status.Conditions, string(conditionType))
	return cond != nil && cond.Status == status
}
