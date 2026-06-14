package helpers

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultPollInterval = 5 * time.Second

func WaitFor[T client.Object](ctx context.Context, c client.Client, key client.ObjectKey, obj T, predicate func(T) bool, timeout time.Duration) (T, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(defaultPollInterval)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			_ = c.Get(ctx, key, obj)
			return obj, fmt.Errorf("timed out after %s waiting for %T %s", timeout, obj, key)
		case <-tick.C:
			if err := c.Get(ctx, key, obj); err != nil {
				continue
			}
			if predicate(obj) {
				return obj, nil
			}
		}
	}
}

func WaitForDeleted(ctx context.Context, c client.Client, key client.ObjectKey, obj client.Object, timeout time.Duration) error {
	deadline := time.After(timeout)
	tick := time.NewTicker(defaultPollInterval)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timed out after %s waiting for %T %s to be deleted", timeout, obj, key)
		case <-tick.C:
			if err := c.Get(ctx, key, obj); errors.IsNotFound(err) {
				return nil
			}
		}
	}
}

// Wait for the ResourceVersion of the object to change from the previous value
func WaitForRVChange[T client.Object](ctx context.Context, c client.Client, key client.ObjectKey, obj T, previousRV string, timeout time.Duration) (T, error) {
	return WaitFor(ctx, c, key, obj, func(o T) bool {
		return o.GetResourceVersion() != previousRV
	}, timeout)
}

// VerifyRVStable checks that the ResourceVersion of the object does not change for the duration of the window, which can help detect spurious updates.
// It returns an error if a change is detected or if the object cannot be retrieved.
func VerifyRVStable(ctx context.Context, c client.Client, key client.ObjectKey, obj client.Object, expectedRV string, window time.Duration) error {
	deadline := time.After(window)
	tick := time.NewTicker(defaultPollInterval)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return nil
		case <-tick.C:
			if err := c.Get(ctx, key, obj); err != nil {
				return fmt.Errorf("failed to get %T %s: %w", obj, key, err)
			}
			if obj.GetResourceVersion() != expectedRV {
				return fmt.Errorf("%T %s ResourceVersion changed from %s to %s (spurious update detected)",
					obj, key, expectedRV, obj.GetResourceVersion())
			}
		}
	}
}
