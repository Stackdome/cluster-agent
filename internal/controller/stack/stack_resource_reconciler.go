package stack

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

func (r *StackReconciler) ReconcileStackResources(ctx context.Context, stack *v1alpha1.Stack) (subReconcilerResult, error) {
	desiredStackResources := make([]*v1alpha1.StackResource, 0)
	for _, stackResourceTemplate := range stack.Spec.StackResources {
		desiredStackResource := constructStackResourceCR(stack, &stackResourceTemplate)
		desiredStackResources = append(desiredStackResources, desiredStackResource)
	}

	existingStackResources := make([]*v1alpha1.StackResource, 0)
	for _, desiredStackResource := range desiredStackResources {
		existingStackResource, err := r.reconcileStackResource(ctx, stack, desiredStackResource)
		if err != nil {
			return resultNil, err
		}
		existingStackResources = append(existingStackResources, existingStackResource)
	}

	for _, sr := range existingStackResources {
		if !r.stackResourceAvailable(sr) {
			reportStackNotReady(stack, "StackResourcesNotReady", fmt.Sprintf("StackResource: '%s' not ready", sr.Name))
			return resultNil, nil
		}
	}
	reportStackReady(stack)
	return resultNil, nil
}

func (r *StackReconciler) stackResourceAvailable(sr *v1alpha1.StackResource) bool {
	availableCond := meta.FindStatusCondition(sr.Status.Conditions, string(v1alpha1.StackResourceStatusAvailable))
	if availableCond != nil && availableCond.Status == v1.ConditionTrue && availableCond.ObservedGeneration == sr.Generation {
		return true
	}
	return false
}

func (r *StackReconciler) reconcileStackResource(
	ctx context.Context,
	stack *v1alpha1.Stack,
	desiredSR *v1alpha1.StackResource) (*v1alpha1.StackResource, error) {
	if err := controllerutil.SetControllerReference(stack, desiredSR, r.Scheme); err != nil {
		return nil, err
	}

	existingSR := &v1alpha1.StackResource{}
	if err := r.Client.Get(ctx, controller.GetNamespacedName(desiredSR), existingSR); err != nil {
		if apierrors.IsNotFound(err) {
			return desiredSR, r.Client.Create(ctx, desiredSR)
		}
		return nil, err
	}

	if !equality.Semantic.DeepDerivative(desiredSR.Spec, existingSR.Spec) {
		existingSR.Spec = desiredSR.Spec
		return existingSR, r.Client.Update(ctx, existingSR)
	}
	return existingSR, nil
}

func constructStackResourceCR(stack *v1alpha1.Stack, stackResourceTemplate *v1alpha1.StackResourceTemplate) *v1alpha1.StackResource {
	return &v1alpha1.StackResource{
		ObjectMeta: v1.ObjectMeta{
			Name:        stackResourceTemplate.Name,
			Namespace:   stack.Namespace,
			Labels:      stack.Labels,
			Annotations: stack.Annotations,
		},
		Spec: v1alpha1.StackResourceSpec{
			BuildSpec:            stackResourceTemplate.Spec.BuildSpec,
			ImageSpec:            stackResourceTemplate.Spec.ImageSpec,
			EnvironmentVariables: stackResourceTemplate.Spec.EnvironmentVariables,
			VolumeMounts:         stackResourceTemplate.Spec.VolumeMounts,
			Ports:                stackResourceTemplate.Spec.Ports,
			Command:              stackResourceTemplate.Spec.Command,
			Init:                 stackResourceTemplate.Spec.Init,
			Args:                 stackResourceTemplate.Spec.Args,
			DependsOn:            stackResourceTemplate.Spec.DependsOn,
			RestartRequest:       stackResourceTemplate.Spec.RestartRequest,
			StateFul:             stackResourceTemplate.Spec.StateFul,
		},
	}
}
