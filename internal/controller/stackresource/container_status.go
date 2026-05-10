package stackresource

import (
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

var crashReasons = map[string]bool{
	"CrashLoopBackOff":     true,
	"ImagePullBackOff":     true,
	"ErrImagePull":         true,
	"CreateContainerError": true,
	"OOMKilled":            true,
	"Error":                true,
}

func isCrashState(cs corev1.ContainerStatus) bool {
	if cs.State.Waiting != nil && crashReasons[cs.State.Waiting.Reason] {
		return true
	}
	if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
		return true
	}
	return false
}

func hasAnyCrashingContainer(pod *corev1.Pod) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if isCrashState(cs) {
			return true
		}
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if isCrashState(cs) {
			return true
		}
	}
	return false
}

func buildFailedContainerStatus(cs corev1.ContainerStatus) v1alpha1.FailedContainerStatus {
	fcs := v1alpha1.FailedContainerStatus{
		Name:         cs.Name,
		RestartCount: cs.RestartCount,
	}

	switch {
	case cs.State.Waiting != nil:
		fcs.State = "waiting"
		fcs.Reason = cs.State.Waiting.Reason
		fcs.Message = cs.State.Waiting.Message
		if cs.LastTerminationState.Terminated != nil {
			t := cs.LastTerminationState.Terminated
			fcs.ExitCode = ptr.To(t.ExitCode)
			fcs.StartedAt = &metav1.Time{Time: t.StartedAt.Time}
			fcs.FinishedAt = &metav1.Time{Time: t.FinishedAt.Time}
		}
	case cs.State.Terminated != nil:
		fcs.State = "terminated"
		fcs.Reason = cs.State.Terminated.Reason
		fcs.Message = cs.State.Terminated.Message
		fcs.ExitCode = ptr.To(cs.State.Terminated.ExitCode)
		fcs.StartedAt = &metav1.Time{Time: cs.State.Terminated.StartedAt.Time}
		fcs.FinishedAt = &metav1.Time{Time: cs.State.Terminated.FinishedAt.Time}
	}

	return fcs
}

func fetchContainerLogs(ctx context.Context, kubeClient kubernetes.Interface, namespace, podName, containerName string, previous bool) string {
	var tailLines int64 = 50
	opts := &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &tailLines,
		Previous:  previous,
	}
	req := kubeClient.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return ""
	}
	defer stream.Close()
	body, err := io.ReadAll(stream)
	if err != nil {
		return ""
	}
	return string(body)
}

func shouldFetchLogs(cs corev1.ContainerStatus) (fetch bool, previous bool) {
	if cs.State.Waiting != nil {
		switch cs.State.Waiting.Reason {
		case "CrashLoopBackOff":
			return true, true
		case "ImagePullBackOff", "ErrImagePull", "CreateContainerError":
			return false, false
		}
		if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.ExitCode != 0 {
			return true, true
		}
	}
	if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
		if cs.RestartCount > 0 {
			return true, true
		}
		return true, false
	}
	return false, false
}

func captureFailedContainerStatuses(ctx context.Context, kubeClient kubernetes.Interface, resource *v1alpha1.StackResource) {
	logger := controller.LoggerFromContext(ctx)

	labels := GetDeploymentPodLabelForResource(resource)
	labelSelector := fmt.Sprintf("resource=%s", labels["resource"])
	podList, err := kubeClient.CoreV1().Pods(resource.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		logger.Error(err, "failed to list pods for container status capture")
		return
	}

	if len(podList.Items) == 0 {
		return
	}

	var crashingPod *corev1.Pod
	for i := range podList.Items {
		if hasAnyCrashingContainer(&podList.Items[i]) {
			crashingPod = &podList.Items[i]
			break
		}
	}

	if crashingPod == nil {
		return
	}

	statuses := make([]v1alpha1.FailedContainerStatus, 0)

	allContainerStatuses := append(crashingPod.Status.InitContainerStatuses, crashingPod.Status.ContainerStatuses...)
	for _, cs := range allContainerStatuses {
		fcs := buildFailedContainerStatus(cs)

		if isCrashState(cs) {
			fetch, previous := shouldFetchLogs(cs)
			if fetch {
				fcs.Logs = fetchContainerLogs(ctx, kubeClient, resource.Namespace, crashingPod.Name, cs.Name, previous)
			}
		}

		statuses = append(statuses, fcs)
	}

	resource.Status.FailedContainerStatuses = statuses
}
