package v1alpha1

import (
	"regexp"
	"strings"
	"testing"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

// A Job name must be a DNS-1123 label: the Job controller copies it verbatim into
// the `batch.kubernetes.io/job-name` label on every pod it creates, and label
// values top out at 63 characters.
var dns1123LabelRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func imageBuildFor(resourceName, revision string) *ImageBuild {
	return &ImageBuild{
		Spec: ImageBuildSpec{
			ResourceName: resourceName,
			SourceRevision: corev1alpha1.SourceRevisionSpec{
				Volume: &corev1alpha1.VolumeRevision{RevisionString: revision},
			},
			BuildContext: BuildContextSpec{
				DockerfilePath: "Dockerfile",
				ContextPath:    "/",
			},
		},
	}
}

func assertValidJobName(t *testing.T, name string) {
	t.Helper()
	if len(name) > maxLabelValueLen {
		t.Errorf("BuildJobName() = %q (len %d), exceeds k8s label limit %d", name, len(name), maxLabelValueLen)
	}
	if !dns1123LabelRegex.MatchString(name) {
		t.Errorf("BuildJobName() = %q, not a valid DNS-1123 label", name)
	}
}

func TestBuildJobName_HonorsK8sNameLimits(t *testing.T) {
	tests := []struct {
		name         string
		resourceName string
		revision     string
	}{
		{"short name", "my-app", "abc123def456"},
		{"full 40-char SHA", "frontend", "20d73f323a4d95ff5a3847717892e1740a5a81b6"},
		{"long resource name", "broken-app-broken-dockerfile", "20d73f323a4d95ff5a3847717892e1740a5a81b6"},
		{"very long resource name gets truncated", strings.Repeat("a", 200), "abc123def456"},
		{"resource name truncated onto a dash", strings.Repeat("ab", 24) + "-tail", "abc"},
		{"branch with slash and uppercase", "app", "Feature/Cool-Stuff"},
		{"branch with dots and underscores", "app", "v1.2.3_beta"},
		{"non-ASCII UTF-8 branch name", "app", "feature/日本語"},
		{"empty resource name falls back", "", "abc123def456"},
		{"empty revision", "app", ""},
		{"resource name of only separators", "___", "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertValidJobName(t, imageBuildFor(tt.resourceName, tt.revision).BuildJobName())
		})
	}
}

func TestBuildJobName_ReadablePrefix(t *testing.T) {
	got := imageBuildFor("broken-app-broken-dockerfile", "abc123def456").BuildJobName()
	if !strings.HasPrefix(got, "broken-app-broken-dockerfile-") || !strings.HasSuffix(got, "-build") {
		t.Errorf("BuildJobName() = %q, want resource-name prefix and -build suffix", got)
	}
}

func TestBuildJobName_Deterministic(t *testing.T) {
	a := imageBuildFor("my-app", "abc123def456").BuildJobName()
	b := imageBuildFor("my-app", "abc123def456").BuildJobName()
	if a != b {
		t.Errorf("BuildJobName is not deterministic: %q != %q", a, b)
	}
}

func TestBuildJobName_CancelledIsNotABuildInput(t *testing.T) {
	base := imageBuildFor("app", "abc123def456")
	cancelled := imageBuildFor("app", "abc123def456")
	cancelled.Spec.Cancelled = true
	if base.BuildJobName() != cancelled.BuildJobName() {
		t.Errorf("cancelling should not change the job name: %q != %q", base.BuildJobName(), cancelled.BuildJobName())
	}
}

func TestBuildJobName_DifferentInputsProduceDifferentNames(t *testing.T) {
	base := imageBuildFor("app", "abc123def456")

	tests := []struct {
		name   string
		mutate func(b *ImageBuild)
	}{
		{"resource name", func(b *ImageBuild) { b.Spec.ResourceName = "other-app" }},
		{"source revision", func(b *ImageBuild) {
			b.Spec.SourceRevision.Volume.RevisionString = "def456abc123"
		}},
		// The bug this covers: same commit, different Dockerfile. Before, both
		// builds mapped to one Job and the second adopted the first's result.
		{"dockerfile path", func(b *ImageBuild) { b.Spec.BuildContext.DockerfilePath = "worker/Dockerfile" }},
		{"context path", func(b *ImageBuild) { b.Spec.BuildContext.ContextPath = "hello-stack/worker" }},
		{"build args", func(b *ImageBuild) {
			b.Spec.BuildArgs = []corev1alpha1.BuildArg{{Name: "MODE", Value: "debug"}}
		}},
		{"repository", func(b *ImageBuild) { b.Spec.Repository.Repository = "other-repo" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := imageBuildFor("app", "abc123def456")
			tt.mutate(changed)
			if got := changed.BuildJobName(); got == base.BuildJobName() {
				t.Errorf("%s change should produce a different job name, both = %q", tt.name, got)
			}
			assertValidJobName(t, changed.BuildJobName())
		})
	}
}

func TestBuildJobName_BranchCollisionFixed(t *testing.T) {
	// Truncation used to make these identical.
	a := imageBuildFor("app", "create-stackfile-42010165d15be77adc3a6ae05563a40e5ca9bb5d").BuildJobName()
	b := imageBuildFor("app", "create-stackfile-e3eb341d3a9aa5a89e7c4a0cc4fd7c9e10d7d58d").BuildJobName()
	if a == b {
		t.Errorf("different commits on same long branch should produce different names, both = %q", a)
	}
}

func imageBuildTestSpec(revision string) *corev1alpha1.StackResourceBuildSpec {
	return &corev1alpha1.StackResourceBuildSpec{
		BuildContext:   "/",
		DockerFilePath: "Dockerfile",
		SourceRevision: corev1alpha1.SourceRevisionSpec{
			Volume: &corev1alpha1.VolumeRevision{RevisionString: revision},
		},
	}
}

func TestImageBuildName(t *testing.T) {
	t.Run("readable prefix and hash suffix", func(t *testing.T) {
		got := ImageBuildName("todo-app", imageBuildTestSpec("feature/auth-implementation"))
		if !strings.HasPrefix(got, "todo-app-feature-auth-implementation-") {
			t.Errorf("ImageBuildName() = %q, want prefix %q", got, "todo-app-feature-auth-implementation-")
		}
		if len(got) > imageBuildNameMaxLen {
			t.Errorf("ImageBuildName() = %q (len %d), exceeds max %d", got, len(got), imageBuildNameMaxLen)
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		a := ImageBuildName("app", imageBuildTestSpec("rev-1"))
		b := ImageBuildName("app", imageBuildTestSpec("rev-1"))
		if a != b {
			t.Errorf("not deterministic: %q != %q", a, b)
		}
	})

	t.Run("build input change produces a new name on the same revision", func(t *testing.T) {
		base := ImageBuildName("app", imageBuildTestSpec("rev-1"))
		changed := imageBuildTestSpec("rev-1")
		changed.BuildContext = "hello-stack/worker"
		if got := ImageBuildName("app", changed); got == base {
			t.Errorf("build context change should change the name, both = %q", got)
		}
	})

	t.Run("revision change survives truncation of the readable revision", func(t *testing.T) {
		longName := strings.Repeat("a", 90)
		a := ImageBuildName(longName, imageBuildTestSpec("branch-"+strings.Repeat("x", 60)+"-sha1111"))
		b := ImageBuildName(longName, imageBuildTestSpec("branch-"+strings.Repeat("x", 60)+"-sha2222"))
		if a == b {
			t.Errorf("different revisions should produce different names after truncation, both = %q", a)
		}
		if len(a) > imageBuildNameMaxLen {
			t.Errorf("ImageBuildName() = %q (len %d), exceeds max %d", a, len(a), imageBuildNameMaxLen)
		}
	})
}
