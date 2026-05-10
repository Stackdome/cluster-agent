package imagebuild

import (
	"context"
	"fmt"
	"io"
	"sort"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

func captureBuildFailureDetail(ctx context.Context, kubeClient kubernetes.Interface, buildConfig *buildsv1alpha1.ImageBuild, job *batchv1.Job) {
	logger := controller.LoggerFromContext(ctx)

	labelSelector := fmt.Sprintf("job-name=%s", job.Name)
	podList, err := kubeClient.CoreV1().Pods(buildConfig.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		logger.Error(err, "failed to list pods for build failure capture")
		return
	}

	if len(podList.Items) == 0 {
		return
	}

	sort.Slice(podList.Items, func(i, j int) bool {
		return podList.Items[j].CreationTimestamp.Before(&podList.Items[i].CreationTimestamp)
	})
	pod := &podList.Items[0]

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != "kaniko" {
			continue
		}

		detail := &buildsv1alpha1.BuildFailureDetail{}

		switch {
		case cs.State.Waiting != nil:
			detail.State = "waiting"
			detail.Reason = cs.State.Waiting.Reason
			detail.Message = cs.State.Waiting.Message
		case cs.State.Terminated != nil:
			detail.State = "terminated"
			detail.Reason = cs.State.Terminated.Reason
			detail.Message = cs.State.Terminated.Message
			detail.ExitCode = ptr.To(cs.State.Terminated.ExitCode)
			detail.StartedAt = &metav1.Time{Time: cs.State.Terminated.StartedAt.Time}
			detail.FinishedAt = &metav1.Time{Time: cs.State.Terminated.FinishedAt.Time}

			detail.Logs = fetchBuildLogs(ctx, kubeClient, buildConfig.Namespace, pod.Name)
		}

		if cs.State.Waiting != nil && cs.LastTerminationState.Terminated != nil {
			t := cs.LastTerminationState.Terminated
			detail.ExitCode = ptr.To(t.ExitCode)
			detail.StartedAt = &metav1.Time{Time: t.StartedAt.Time}
			detail.FinishedAt = &metav1.Time{Time: t.FinishedAt.Time}
			detail.Logs = fetchBuildLogs(ctx, kubeClient, buildConfig.Namespace, pod.Name)
		}

		if detail.State != "" {
			buildConfig.Status.BuildFailureDetail = detail
		}
		return
	}
}

func fetchBuildLogs(ctx context.Context, kubeClient kubernetes.Interface, namespace, podName string) string {
	var tailLines int64 = 100
	opts := &corev1.PodLogOptions{
		Container: "kaniko",
		TailLines: &tailLines,
		Previous:  true,
	}
	req := kubeClient.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		opts.Previous = false
		req = kubeClient.CoreV1().Pods(namespace).GetLogs(podName, opts)
		stream, err = req.Stream(ctx)
		if err != nil {
			return ""
		}
	}
	defer stream.Close()
	body, err := io.ReadAll(stream)
	if err != nil {
		return ""
	}
	return string(body)
}
