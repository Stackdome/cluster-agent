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
	build, err := WaitFor(ctx, c, key, &buildsv1alpha1.ImageBuild{}, func(b *buildsv1alpha1.ImageBuild) bool {
		return b.Status.Phase == buildsv1alpha1.BuildPhaseSuccess || b.Status.Phase == buildsv1alpha1.BuildPhaseFailed
	}, timeout)
	if err != nil {
		return build, err
	}
	if build.Status.Phase == buildsv1alpha1.BuildPhaseFailed {
		return build, fmt.Errorf("ImageBuild %s failed", key.Name)
	}
	return build, nil
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
	build, err := WaitFor(ctx, c, key, &buildsv1alpha1.ImageBuild{}, func(b *buildsv1alpha1.ImageBuild) bool {
		return b.Status.Phase == buildsv1alpha1.BuildPhaseFailed || b.Status.Phase == buildsv1alpha1.BuildPhaseSuccess
	}, timeout)
	if err != nil {
		return build, err
	}
	if build.Status.Phase == buildsv1alpha1.BuildPhaseSuccess {
		return build, fmt.Errorf("ImageBuild %s succeeded unexpectedly", key.Name)
	}
	return build, nil
}

func WaitForBuildFailureDetail(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*buildsv1alpha1.ImageBuild, error) {
	return WaitFor(ctx, c, key, &buildsv1alpha1.ImageBuild{}, func(b *buildsv1alpha1.ImageBuild) bool {
		return b.Status.LastBuildFailureDetail != nil
	}, timeout)
}
