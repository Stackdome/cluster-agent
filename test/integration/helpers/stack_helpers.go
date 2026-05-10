package helpers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

// WaitForStackReady polls until the Stack reaches phase=Ready or the timeout expires.
func WaitForStackReady(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*corev1alpha1.Stack, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			stack := &corev1alpha1.Stack{}
			_ = c.Get(ctx, key, stack)
			return stack, fmt.Errorf("timed out waiting for Stack %s to become Ready (current phase: %s)", key.Name, stack.Status.Phase)
		case <-tick.C:
			stack := &corev1alpha1.Stack{}
			if err := c.Get(ctx, key, stack); err != nil {
				continue
			}
			if stack.Status.Phase == corev1alpha1.StackReady {
				return stack, nil
			}
		}
	}
}

// WaitForStackResourceAvailable polls until the StackResource has Available=True.
func WaitForStackResourceAvailable(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*corev1alpha1.StackResource, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			sr := &corev1alpha1.StackResource{}
			_ = c.Get(ctx, key, sr)
			return sr, fmt.Errorf("timed out waiting for StackResource %s to become Available (current phase: %s)", key.Name, sr.Status.Phase)
		case <-tick.C:
			sr := &corev1alpha1.StackResource{}
			if err := c.Get(ctx, key, sr); err != nil {
				continue
			}
			cond := meta.FindStatusCondition(sr.Status.Conditions, string(corev1alpha1.StackResourceStatusAvailable))
			if cond != nil && cond.Status == metav1.ConditionTrue {
				return sr, nil
			}
		}
	}
}

// WaitForStackDeleted polls until the Stack no longer exists.
func WaitForStackDeleted(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) error {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timed out waiting for Stack %s to be deleted", key.Name)
		case <-tick.C:
			stack := &corev1alpha1.Stack{}
			if err := c.Get(ctx, key, stack); err != nil {
				if errors.IsNotFound(err) {
					return nil
				}
			}
		}
	}
}

// WaitForStackResourceDeleted polls until the StackResource no longer exists.
func WaitForStackResourceDeleted(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) error {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timed out waiting for StackResource %s to be deleted", key.Name)
		case <-tick.C:
			sr := &corev1alpha1.StackResource{}
			if err := c.Get(ctx, key, sr); err != nil {
				if errors.IsNotFound(err) {
					return nil
				}
			}
		}
	}
}

// GetDeploymentForResource retrieves the Deployment created for a StackResource.
func GetDeploymentForResource(ctx context.Context, c client.Client, namespace, resourceName string) (*appsv1.Deployment, error) {
	dep := &appsv1.Deployment{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      resourceName,
		Namespace: namespace,
	}, dep)
	return dep, err
}

// GetServiceForResource retrieves the Service created for a StackResource.
func GetServiceForResource(ctx context.Context, c client.Client, namespace, resourceName string) (*corev1.Service, error) {
	svc := &corev1.Service{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      resourceName,
		Namespace: namespace,
	}, svc)
	return svc, err
}

// StackResourceIsAvailable checks whether a StackResource has Available=True.
func StackResourceIsAvailable(sr *corev1alpha1.StackResource) bool {
	cond := meta.FindStatusCondition(sr.Status.Conditions, string(corev1alpha1.StackResourceStatusAvailable))
	return cond != nil && cond.Status == metav1.ConditionTrue
}

// DeploymentExists checks if a Deployment exists for the given resource name.
func DeploymentExists(ctx context.Context, c client.Client, namespace, resourceName string) bool {
	dep := &appsv1.Deployment{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      resourceName,
		Namespace: namespace,
	}, dep)
	return err == nil
}

// GetContainerEnvVar finds an env var by name in the first container of a Deployment.
func GetContainerEnvVar(dep *appsv1.Deployment, envName string) (string, bool) {
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return "", false
	}
	for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
		if env.Name == envName {
			return env.Value, true
		}
	}
	return "", false
}

// GetIngressForResource retrieves the Ingress created for a StackResource.
func GetIngressForResource(ctx context.Context, c client.Client, namespace, resourceName string) (*networkingv1.Ingress, error) {
	ingress := &networkingv1.Ingress{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      resourceName + "-http-proxy",
		Namespace: namespace,
	}, ingress)
	return ingress, err
}

// GetStackResource retrieves a StackResource by key.
func GetStackResource(ctx context.Context, c client.Client, key client.ObjectKey) (*corev1alpha1.StackResource, error) {
	sr := &corev1alpha1.StackResource{}
	err := c.Get(ctx, key, sr)
	return sr, err
}

// WaitForDeploymentUpdated polls until the Deployment matches the given predicate.
func WaitForDeploymentUpdated(ctx context.Context, c client.Client, namespace, name string, predicate func(*appsv1.Deployment) bool, timeout time.Duration) (*appsv1.Deployment, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			dep, _ := GetDeploymentForResource(ctx, c, namespace, name)
			return dep, fmt.Errorf("timed out waiting for Deployment %s to match predicate", name)
		case <-tick.C:
			dep, err := GetDeploymentForResource(ctx, c, namespace, name)
			if err != nil {
				continue
			}
			if predicate(dep) {
				return dep, nil
			}
		}
	}
}

// WaitForStackResourceCount polls until the namespace has at least the given number of StackResources.
func WaitForStackResourceCount(ctx context.Context, c client.Client, namespace string, count int, timeout time.Duration) error {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timed out waiting for %d StackResources in %s", count, namespace)
		case <-tick.C:
			list := &corev1alpha1.StackResourceList{}
			if err := c.List(ctx, list, client.InNamespace(namespace)); err != nil {
				continue
			}
			if len(list.Items) >= count {
				return nil
			}
		}
	}
}

// HasOwnerReference checks whether an object has an owner reference with the given name and kind.
func HasOwnerReference(obj metav1.ObjectMeta, ownerName, ownerKind string) bool {
	for _, ref := range obj.OwnerReferences {
		if ref.Name == ownerName && ref.Kind == ownerKind {
			return true
		}
	}
	return false
}

// GetPodTemplateAnnotation retrieves an annotation from a Deployment's pod template.
func GetPodTemplateAnnotation(dep *appsv1.Deployment, key string) (string, bool) {
	if dep.Spec.Template.Annotations == nil {
		return "", false
	}
	val, ok := dep.Spec.Template.Annotations[key]
	return val, ok
}

// ServiceIsHeadless checks whether a Service has ClusterIP set to "None".
func ServiceIsHeadless(svc *corev1.Service) bool {
	return svc.Spec.ClusterIP == "None"
}

func WaitForFailedContainerStatuses(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*corev1alpha1.StackResource, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			sr := &corev1alpha1.StackResource{}
			_ = c.Get(ctx, key, sr)
			return sr, fmt.Errorf("timed out waiting for FailedContainerStatuses on StackResource %s", key.Name)
		case <-tick.C:
			sr := &corev1alpha1.StackResource{}
			if err := c.Get(ctx, key, sr); err != nil {
				continue
			}
			if len(sr.Status.FailedContainerStatuses) > 0 {
				return sr, nil
			}
		}
	}
}
