package helpers

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
)

// WaitForPostgresClusterReady polls until the PostgresCluster reaches ClusterReady=True
// or the timeout expires.
func WaitForPostgresClusterReady(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*addonsv1alpha1.PostgresCluster, error) {
	return WaitFor(ctx, c, key, &addonsv1alpha1.PostgresCluster{}, func(pg *addonsv1alpha1.PostgresCluster) bool {
		cond := meta.FindStatusCondition(pg.Status.Conditions, string(addonsv1alpha1.ClusterReady))
		return cond != nil && cond.Status == metav1.ConditionTrue
	}, timeout)
}

// WaitForCondition polls until a specific condition is set on the PostgresCluster.
func WaitForPostgresClusterCondition(ctx context.Context, c client.Client, key client.ObjectKey, conditionType addonsv1alpha1.PostgresClusterConditionType, expectedStatus metav1.ConditionStatus, timeout time.Duration) (*addonsv1alpha1.PostgresCluster, error) {
	return WaitFor(ctx, c, key, &addonsv1alpha1.PostgresCluster{}, func(pg *addonsv1alpha1.PostgresCluster) bool {
		cond := meta.FindStatusCondition(pg.Status.Conditions, string(conditionType))
		return cond != nil && cond.Status == expectedStatus
	}, timeout)
}

// WaitForPostgresClusterDeleted polls until the PostgresCluster no longer exists.
func WaitForPostgresClusterDeleted(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) error {
	return WaitForDeleted(ctx, c, key, &addonsv1alpha1.PostgresCluster{}, timeout)
}

// HasCondition checks if a PostgresCluster has a specific condition with the expected status.
func PostgresClusterHasCondition(pg *addonsv1alpha1.PostgresCluster, conditionType addonsv1alpha1.PostgresClusterConditionType, status metav1.ConditionStatus) bool {
	cond := meta.FindStatusCondition(pg.Status.Conditions, string(conditionType))
	return cond != nil && cond.Status == status
}
