package helpers

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
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
