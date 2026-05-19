package helpers

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
)

// WaitForImageBuildCreated polls until an ImageBuild CR with the given prefix exists.
func WaitForImageBuildCreated(ctx context.Context, c client.Client, namespace, namePrefix string, timeout time.Duration) (*buildsv1alpha1.ImageBuild, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return nil, fmt.Errorf("timed out waiting for ImageBuild with prefix %q in %s", namePrefix, namespace)
		case <-tick.C:
			list := &buildsv1alpha1.ImageBuildList{}
			if err := c.List(ctx, list, client.InNamespace(namespace)); err != nil {
				continue
			}
			for i := range list.Items {
				if len(list.Items[i].Name) >= len(namePrefix) && list.Items[i].Name[:len(namePrefix)] == namePrefix {
					return &list.Items[i], nil
				}
			}
		}
	}
}

// WaitForImageBuildComplete polls until the ImageBuild reaches Success or Failed phase.
func WaitForImageBuildComplete(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*buildsv1alpha1.ImageBuild, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			build := &buildsv1alpha1.ImageBuild{}
			_ = c.Get(ctx, key, build)
			return build, fmt.Errorf("timed out waiting for ImageBuild %s to complete (current phase: %s)", key.Name, build.Status.Phase)
		case <-tick.C:
			build := &buildsv1alpha1.ImageBuild{}
			if err := c.Get(ctx, key, build); err != nil {
				continue
			}
			if build.Status.Phase == buildsv1alpha1.BuildPhaseSuccess {
				return build, nil
			}
			if build.Status.Phase == buildsv1alpha1.BuildPhaseFailed {
				return build, fmt.Errorf("ImageBuild %s failed", key.Name)
			}
		}
	}
}

// GetBuildJob retrieves the Kaniko build Job for an ImageBuild.
func GetBuildJob(ctx context.Context, c client.Client, namespace, imageBuildName string) (*batchv1.Job, error) {
	job := &batchv1.Job{}
	jobName := fmt.Sprintf("%s-build", imageBuildName)
	err := c.Get(ctx, client.ObjectKey{
		Name:      jobName,
		Namespace: namespace,
	}, job)
	return job, err
}

// WaitForBuildJob polls until the build Job exists for an ImageBuild.
func WaitForBuildJob(ctx context.Context, c client.Client, namespace, imageBuildName string, timeout time.Duration) (*batchv1.Job, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	jobName := fmt.Sprintf("%s-build", imageBuildName)
	for {
		select {
		case <-deadline:
			return nil, fmt.Errorf("timed out waiting for build Job %s", jobName)
		case <-tick.C:
			job, err := GetBuildJob(ctx, c, namespace, imageBuildName)
			if err == nil {
				return job, nil
			}
		}
	}
}

// JobHasBuildArg checks if a Job's kaniko container has a specific --build-arg flag.
func JobHasBuildArg(job *batchv1.Job, argName, argValue string) bool {
	expected := fmt.Sprintf("--build-arg=%s=%s", argName, argValue)
	for _, container := range job.Spec.Template.Spec.Containers {
		for _, arg := range container.Args {
			if arg == expected {
				return true
			}
		}
	}
	return false
}

// WaitForImageBuildFailed polls until the ImageBuild reaches Failed phase.
func WaitForImageBuildFailed(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*buildsv1alpha1.ImageBuild, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			build := &buildsv1alpha1.ImageBuild{}
			_ = c.Get(ctx, key, build)
			return build, fmt.Errorf("timed out waiting for ImageBuild %s to fail (current phase: %s)", key.Name, build.Status.Phase)
		case <-tick.C:
			build := &buildsv1alpha1.ImageBuild{}
			if err := c.Get(ctx, key, build); err != nil {
				continue
			}
			if build.Status.Phase == buildsv1alpha1.BuildPhaseFailed {
				return build, nil
			}
			if build.Status.Phase == buildsv1alpha1.BuildPhaseSuccess {
				return build, fmt.Errorf("ImageBuild %s succeeded unexpectedly", key.Name)
			}
		}
	}
}

func WaitForBuildFailureDetail(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*buildsv1alpha1.ImageBuild, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			build := &buildsv1alpha1.ImageBuild{}
			_ = c.Get(ctx, key, build)
			return build, fmt.Errorf("timed out waiting for BuildFailureDetail on ImageBuild %s", key.Name)
		case <-tick.C:
			build := &buildsv1alpha1.ImageBuild{}
			if err := c.Get(ctx, key, build); err != nil {
				continue
			}
			if build.Status.LastBuildFailureDetail != nil {
				return build, nil
			}
		}
	}
}

// DumpBuildDiagnostics returns a summary of ImageBuild CRs, build Jobs, build
// Pods, and the last 50 lines of each build pod's logs in the given namespace.
// Call before cleanup so failures leave a trail in the test log.
func DumpBuildDiagnostics(ctx context.Context, c client.Client, kubeClient kubernetes.Interface, namespace string) string {
	out := "\n=== Build Diagnostics ===\n"

	builds := &buildsv1alpha1.ImageBuildList{}
	if err := c.List(ctx, builds, client.InNamespace(namespace)); err != nil {
		out += fmt.Sprintf("failed to list ImageBuilds: %v\n", err)
	} else {
		for _, b := range builds.Items {
			out += fmt.Sprintf("\nImageBuild: %s\n  Phase: %s\n  Conditions:\n", b.Name, b.Status.Phase)
			for _, cond := range b.Status.Conditions {
				out += fmt.Sprintf("    %s=%s (reason=%s, message=%s)\n", cond.Type, cond.Status, cond.Reason, cond.Message)
			}
			if d := b.Status.LastBuildFailureDetail; d != nil {
				out += fmt.Sprintf("  LastBuildFailureDetail:\n    container=%s reason=%s exitCode=%s\n    message=%s\n",
					d.ContainerName, d.LastTerminationReason, fmtInt32Ptr(d.LastTerminationExitCode), d.LastTerminationMessage)
			}
		}
	}

	stackResources := &corev1alpha1.StackResourceList{}
	if err := c.List(ctx, stackResources, client.InNamespace(namespace)); err != nil {
		out += fmt.Sprintf("failed to list StackResources: %v\n", err)
	} else {
		for _, sr := range stackResources.Items {
			out += fmt.Sprintf("\nStackResource: %s\n  Phase: %s\n", sr.Name, sr.Status.Phase)
			for _, cond := range sr.Status.Conditions {
				out += fmt.Sprintf("  Condition %s=%s (reason=%s, message=%s)\n", cond.Type, cond.Status, cond.Reason, cond.Message)
			}
			if sr.Status.CurrentBuild != nil {
				cb := sr.Status.CurrentBuild
				out += fmt.Sprintf("  CurrentBuild: name=%s phase=%s available=%v reason=%s message=%s\n",
					cb.Name, cb.Phase, cb.Available, cb.Reason, cb.Message)
			}
			for _, fd := range sr.Status.LastFailureDetails {
				out += fmt.Sprintf("  LastFailureDetail: container=%s restarts=%d reason=%s exitCode=%s\n    message=%s\n",
					fd.ContainerName, fd.RestartCount, fd.LastTerminationReason, fmtInt32Ptr(fd.LastTerminationExitCode), fd.LastTerminationMessage)
			}
		}
	}

	jobs := &batchv1.JobList{}
	if err := c.List(ctx, jobs, client.InNamespace(namespace)); err != nil {
		out += fmt.Sprintf("failed to list Jobs: %v\n", err)
	} else {
		for _, j := range jobs.Items {
			out += fmt.Sprintf("\nJob: %s\n  Active=%d Succeeded=%d Failed=%d\n  Conditions:\n",
				j.Name, j.Status.Active, j.Status.Succeeded, j.Status.Failed)
			for _, cond := range j.Status.Conditions {
				out += fmt.Sprintf("    %s=%s (reason=%s, message=%s)\n", cond.Type, string(cond.Status), cond.Reason, cond.Message)
			}
		}
	}

	pods := &corev1.PodList{}
	if err := c.List(ctx, pods, client.InNamespace(namespace)); err != nil {
		out += fmt.Sprintf("failed to list Pods: %v\n", err)
	} else {
		for _, p := range pods.Items {
			out += fmt.Sprintf("\nPod: %s\n  Phase: %s\n", p.Name, p.Status.Phase)
			for _, cs := range p.Status.ContainerStatuses {
				out += fmt.Sprintf("  Container %s: ready=%v", cs.Name, cs.Ready)
				if cs.State.Waiting != nil {
					out += fmt.Sprintf(" waiting(reason=%s, message=%s)", cs.State.Waiting.Reason, cs.State.Waiting.Message)
				}
				if cs.State.Terminated != nil {
					out += fmt.Sprintf(" terminated(reason=%s, exitCode=%d)", cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
				}
				out += "\n"
			}
			for _, cs := range p.Status.InitContainerStatuses {
				out += fmt.Sprintf("  InitContainer %s: ready=%v", cs.Name, cs.Ready)
				if cs.State.Waiting != nil {
					out += fmt.Sprintf(" waiting(reason=%s, message=%s)", cs.State.Waiting.Reason, cs.State.Waiting.Message)
				}
				if cs.State.Terminated != nil {
					out += fmt.Sprintf(" terminated(reason=%s, exitCode=%d)", cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
				}
				out += "\n"
			}

			if strings.Contains(p.Name, "build") {
				out += fetchPodLogs(ctx, kubeClient, namespace, p.Name, 50)
			}
		}
	}

	// ClusterRegistry status
	registries := &registryv1alpha1.ClusterRegistryList{}
	if err := c.List(ctx, registries); err != nil {
		out += fmt.Sprintf("\nfailed to list ClusterRegistries: %v\n", err)
	} else {
		for _, r := range registries.Items {
			out += fmt.Sprintf("\nClusterRegistry: %s\n  Phase: %s\n  InternalURL: %s\n  ServiceIP: %s\n  Conditions:\n",
				r.Name, r.Status.Phase, r.Status.InternalURL, r.Status.ServiceIP)
			for _, cond := range r.Status.Conditions {
				out += fmt.Sprintf("    %s=%s (reason=%s, message=%s)\n", cond.Type, cond.Status, cond.Reason, cond.Message)
			}
		}
	}

	// Registry config reconciler DaemonSet status
	ds := &appsv1.DaemonSet{}
	dsKey := client.ObjectKey{Name: "registry-config-reconciler", Namespace: "stackdome-registry"}
	if err := c.Get(ctx, dsKey, ds); err != nil {
		out += fmt.Sprintf("\nfailed to get registry-config-reconciler DaemonSet: %v\n", err)
	} else {
		out += fmt.Sprintf("\nDaemonSet: %s/%s\n  Desired=%d Available=%d Ready=%d\n",
			ds.Namespace, ds.Name, ds.Status.DesiredNumberScheduled, ds.Status.NumberAvailable, ds.Status.NumberReady)
		// DaemonSet pod logs
		dsPods := &corev1.PodList{}
		if err := c.List(ctx, dsPods, client.InNamespace("stackdome-registry"), client.MatchingLabels{"daemonset-for": "registry-config-reconciler"}); err == nil {
			for _, p := range dsPods.Items {
				out += fmt.Sprintf("  Pod %s (node=%s): phase=%s\n", p.Name, p.Spec.NodeName, p.Status.Phase)
				for _, cs := range p.Status.ContainerStatuses {
					out += fmt.Sprintf("    Container %s: ready=%v restarts=%d\n", cs.Name, cs.Ready, cs.RestartCount)
				}
				out += fetchPodLogs(ctx, kubeClient, "stackdome-registry", p.Name, 20)
			}
		}
	}

	out += "=== End Build Diagnostics ===\n"
	return out
}

func fetchPodLogs(ctx context.Context, kubeClient kubernetes.Interface, namespace, podName string, tailLines int64) string {
	out := fmt.Sprintf("  Logs (last %d lines):\n", tailLines)
	req := kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{TailLines: &tailLines})
	stream, err := req.Stream(ctx)
	if err != nil {
		return out + fmt.Sprintf("    <failed to fetch logs: %v>\n", err)
	}
	defer stream.Close()
	body, err := io.ReadAll(stream)
	if err != nil {
		return out + fmt.Sprintf("    <failed to read logs: %v>\n", err)
	}
	if len(body) == 0 {
		return out + "    <no logs available>\n"
	}
	for _, line := range strings.Split(string(body), "\n") {
		out += "    " + line + "\n"
	}
	return out
}

func fmtInt32Ptr(p *int32) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *p)
}
