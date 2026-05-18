package imagebuild

import (
	"context"
	"sort"

	"github.com/samber/lo"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

const buildContainerName = "kaniko"

func getBuildFailureDetail(ctx context.Context, uncachedClient client.Client, buildConfig *buildsv1alpha1.ImageBuild, job *batchv1.Job) (*corev1alpha1.LastFailureDetail, error) {
	podList := &corev1.PodList{}
	err := uncachedClient.List(ctx, podList, client.InNamespace(buildConfig.Namespace), client.MatchingLabels(map[string]string{
		"job-name": job.Name,
	}))
	if err != nil {
		return nil, err
	}

	if len(podList.Items) == 0 {
		return nil, nil
	}

	sort.Slice(podList.Items, func(i, j int) bool {
		return podList.Items[j].CreationTimestamp.Before(&podList.Items[i].CreationTimestamp)
	})
	pod := &podList.Items[0]

	container, found := lo.Find(pod.Status.ContainerStatuses, func(cs corev1.ContainerStatus) bool {
		return cs.Name == buildContainerName
	})
	if !found {
		return nil, nil
	}

	if !controller.IsCrashState(container) {
		return nil, nil
	}

	detail := controller.BuildLastFailureDetail(container)
	return &detail, nil
}
