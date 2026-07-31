package v1alpha1

import (
	"strings"
	"testing"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

func TestBuildJobName_BranchCollisionFixed(t *testing.T) {
	// This is the bug we're fixing - truncation made these identical
	a := BuildJobName("app", "create-stackfile-42010165d15be77adc3a6ae05563a40e5ca9bb5d")
	b := BuildJobName("app", "create-stackfile-e3eb341d3a9aa5a89e7c4a0cc4fd7c9e10d7d58d")
	if a == b {
		t.Errorf("different commits on same long branch should produce different names, both = %q", a)
	}
}

func TestBuildJobName(t *testing.T) {
	tests := []struct {
		name           string
		resourceName   string
		sourceRevision string
		wantName       string
		wantMaxLen     int
	}{
		{
			name:           "short name stays as-is",
			resourceName:   "my-app",
			sourceRevision: "abc123def456",
			wantName:       "my-app-e861b2ea-build",
			wantMaxLen:     63,
		},
		{
			name:           "full 40-char SHA is truncated to 8",
			resourceName:   "frontend",
			sourceRevision: "20d73f323a4d95ff5a3847717892e1740a5a81b6",
			wantName:       "frontend-3bfc284a-build",
			wantMaxLen:     63,
		},
		{
			name:           "long resource name from the issue",
			resourceName:   "broken-app-broken-dockerfile",
			sourceRevision: "20d73f323a4d95ff5a3847717892e1740a5a81b6",
			wantName:       "broken-app-broken-dockerfile-3bfc284a-build",
			wantMaxLen:     63,
		},
		{
			name:           "very long resource name gets truncated",
			resourceName:   "this-is-an-extremely-long-resource-name-that-exceeds-the-limit",
			sourceRevision: "abc123def456",
			wantMaxLen:     63,
		},
		{
			name:           "short revision used as-is (hashed)",
			resourceName:   "app",
			sourceRevision: "abc",
			wantName:       "app-ba7816bf-build",
			wantMaxLen:     63,
		},
		{
			name:           "branch name with slash and uppercase",
			resourceName:   "app",
			sourceRevision: "Feature/Cool-Stuff",
			wantName:       "app-9d30e60e-build",
			wantMaxLen:     63,
		},
		{
			name:           "branch name with dots and underscores",
			resourceName:   "app",
			sourceRevision: "v1.2.3_beta",
			wantName:       "app-0b506316-build",
			wantMaxLen:     63,
		},
		{
			name:           "non-ASCII UTF-8 branch name",
			resourceName:   "app",
			sourceRevision: "feature/日本語",
			wantName:       "app-ffc43221-build",
			wantMaxLen:     63,
		},
		{
			name:           "empty revision falls back to rev",
			resourceName:   "app",
			sourceRevision: "",
			wantName:       "app-rev-build",
			wantMaxLen:     63,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildJobName(tt.resourceName, tt.sourceRevision)
			if len(got) > tt.wantMaxLen {
				t.Errorf("BuildJobName() = %q (len %d), exceeds max %d", got, len(got), tt.wantMaxLen)
			}
			if tt.wantName != "" && got != tt.wantName {
				t.Errorf("BuildJobName() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

func TestBuildJobName_Deterministic(t *testing.T) {
	a := BuildJobName("my-app", "abc123def456")
	b := BuildJobName("my-app", "abc123def456")
	if a != b {
		t.Errorf("BuildJobName is not deterministic: %q != %q", a, b)
	}
}

func TestBuildJobName_DifferentInputsProduceDifferentNames(t *testing.T) {
	a := BuildJobName("app-a", "abc123def456")
	b := BuildJobName("app-b", "abc123def456")
	if a == b {
		t.Errorf("different resource names should produce different job names: both = %q", a)
	}

	c := BuildJobName("app-a", "abc123def456")
	d := BuildJobName("app-a", "def456abc123")
	if c == d {
		t.Errorf("different revisions should produce different job names: both = %q", c)
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
