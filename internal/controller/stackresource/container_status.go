package stackresource

import (
	"context"
	"fmt"
	"slices"

	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

func captureLastFailureDetails(ctx context.Context, uncachedClient client.Client, resource *v1alpha1.StackResource, deploymentRevision string) ([]v1alpha1.LastFailureDetail, error) {
	labels := GetDeploymentLabelForResource(resource)
	replicaSetList := &appsv1.ReplicaSetList{}
	err := uncachedClient.List(ctx, replicaSetList, client.InNamespace(resource.Namespace), client.MatchingLabels(map[string]string{
		"resource": labels["resource"],
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to list replicasets: %w", err)
	}

	replicaSet, found := lo.Find(replicaSetList.Items, func(rs appsv1.ReplicaSet) bool {
		return rs.Annotations[deploymentRevisionAnnotation] == deploymentRevision
	})
	if !found {
		return nil, nil
	}

	podTemplateHash, ok := replicaSet.Labels["pod-template-hash"]
	if !ok {
		return nil, nil
	}

	podList := &corev1.PodList{}
	err = uncachedClient.List(ctx, podList, client.InNamespace(resource.Namespace),
		client.MatchingLabels(replicaSet.Spec.Selector.MatchLabels),
		client.MatchingLabels(map[string]string{
			"pod-template-hash": podTemplateHash,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	if len(podList.Items) == 0 {
		return nil, nil
	}

	slices.SortFunc(podList.Items, func(pod1, pod2 corev1.Pod) int {
		return pod2.CreationTimestamp.Compare(pod1.CreationTimestamp.Time)
	})

	crashingPod, found := lo.Find(podList.Items, func(pod corev1.Pod) bool {
		return hasAnyCrashingContainer(&pod)
	})
	if !found {
		return nil, nil
	}

	var details []v1alpha1.LastFailureDetail
	allContainerStatuses := make([]corev1.ContainerStatus, 0, len(crashingPod.Status.InitContainerStatuses)+len(crashingPod.Status.ContainerStatuses))
	allContainerStatuses = append(allContainerStatuses, crashingPod.Status.InitContainerStatuses...)
	allContainerStatuses = append(allContainerStatuses, crashingPod.Status.ContainerStatuses...)
	for _, cs := range allContainerStatuses {
		if controller.IsCrashState(cs) {
			details = append(details, controller.BuildLastFailureDetail(cs))
		}
	}

	return details, nil
}

func hasAnyCrashingContainer(pod *corev1.Pod) bool {
	isCrashing := func(cs corev1.ContainerStatus) bool {
		return controller.IsCrashState(cs)
	}

	return slices.ContainsFunc(pod.Status.ContainerStatuses, isCrashing) ||
		slices.ContainsFunc(pod.Status.InitContainerStatuses, isCrashing)
}
