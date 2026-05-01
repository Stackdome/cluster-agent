package imagebuilder

import (
	"testing"
)

func TestGenerateImageBuildJob_WithBuildArgs(t *testing.T) {
	params := NewBuildParamsBuilder().
		WithJobName("test-build").
		WithNamespace("default").
		WithRegistryURL("registry.example.com").
		WithImageName("myapp").
		WithTag("v1").
		WithInsecureRegistry(false).
		WithDockerfilePath("Dockerfile").
		WithContextPath("/").
		WithSource(&Source{
			Volume: &VolumeSource{PvcName: "test-pvc"},
		}).
		WithBuildArgs([]ResolvedBuildArg{
			{Name: "BRANCH_NAME", Value: "main"},
			{Name: "CUSTOM_TOKEN", Value: "secret-value"},
		}).
		Build()

	job, err := GenerateImageBuildJob(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	expectedArgs := []string{
		"--build-arg=BRANCH_NAME=main",
		"--build-arg=CUSTOM_TOKEN=secret-value",
	}
	for _, expected := range expectedArgs {
		found := false
		for _, arg := range container.Args {
			if arg == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected arg %q not found in container args: %v", expected, container.Args)
		}
	}
}

func TestGenerateImageBuildJob_NoBuildArgs(t *testing.T) {
	params := NewBuildParamsBuilder().
		WithJobName("test-build").
		WithNamespace("default").
		WithRegistryURL("registry.example.com").
		WithImageName("myapp").
		WithTag("v1").
		WithInsecureRegistry(false).
		WithDockerfilePath("Dockerfile").
		WithContextPath("/").
		WithSource(&Source{
			Volume: &VolumeSource{PvcName: "test-pvc"},
		}).
		Build()

	job, err := GenerateImageBuildJob(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	for _, arg := range container.Args {
		if len(arg) >= 12 && arg[:12] == "--build-arg=" {
			t.Errorf("unexpected --build-arg found: %s", arg)
		}
	}
}
