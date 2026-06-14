package helpers

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

const cleanupTimeout = 2 * time.Minute

// CleanupStack deletes the stack (if non-nil) and waits for it to disappear.
// Errors are swallowed: cleanup runs in AfterAll where the spec verdict is
// already decided and a missing object is success.
func CleanupStack(ctx context.Context, c client.Client, stack *corev1alpha1.Stack) {
	if stack == nil {
		return
	}
	_ = c.Delete(ctx, stack)
	_ = WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), cleanupTimeout)
}
