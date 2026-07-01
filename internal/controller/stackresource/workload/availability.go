package workload

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func deploymentRolledOut(deployment *appsv1.Deployment) bool {
	if deployment.Generation != deployment.Status.ObservedGeneration {
		return false
	}
	return deployment.Status.Replicas == deployment.Status.UpdatedReplicas
}

func deploymentServing(deployment *appsv1.Deployment) bool {
	if deployment.Generation != deployment.Status.ObservedGeneration {
		return false
	}
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func deploymentConverged(deployment *appsv1.Deployment) bool {
	if deployment.Generation != deployment.Status.ObservedGeneration {
		return false
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if deployment.Status.UpdatedReplicas != desired ||
		deployment.Status.ReadyReplicas != desired ||
		deployment.Status.AvailableReplicas != desired ||
		deployment.Status.UnavailableReplicas != 0 {
		return false
	}
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func maxUnavailableForReplicas(replicas int32) int32 {
	if replicas <= 1 {
		return 0
	}
	return max(int32(1), replicas/4)
}
