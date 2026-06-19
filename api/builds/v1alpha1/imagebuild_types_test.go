package v1alpha1

import (
	"testing"
)

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
			wantName:       "my-app-abc123de-build",
			wantMaxLen:     63,
		},
		{
			name:           "full 40-char SHA is truncated to 8",
			resourceName:   "frontend",
			sourceRevision: "20d73f323a4d95ff5a3847717892e1740a5a81b6",
			wantName:       "frontend-20d73f32-build",
			wantMaxLen:     63,
		},
		{
			name:           "long resource name from the issue",
			resourceName:   "broken-app-broken-dockerfile",
			sourceRevision: "20d73f323a4d95ff5a3847717892e1740a5a81b6",
			wantName:       "broken-app-broken-dockerfile-20d73f32-build",
			wantMaxLen:     63,
		},
		{
			name:           "very long resource name gets truncated",
			resourceName:   "this-is-an-extremely-long-resource-name-that-exceeds-the-limit",
			sourceRevision: "abc123def456",
			wantMaxLen:     63,
		},
		{
			name:           "short revision used as-is",
			resourceName:   "app",
			sourceRevision: "abc",
			wantName:       "app-abc-build",
			wantMaxLen:     63,
		},
		{
			name:           "branch name with slash and uppercase",
			resourceName:   "app",
			sourceRevision: "Feature/Cool-Stuff",
			wantName:       "app-feature-build",
			wantMaxLen:     63,
		},
		{
			name:           "branch name with dots and underscores",
			resourceName:   "app",
			sourceRevision: "v1.2.3_beta",
			wantName:       "app-v1-2-3-b-build",
			wantMaxLen:     63,
		},
		{
			name:           "non-ASCII UTF-8 branch name",
			resourceName:   "app",
			sourceRevision: "feature/日本語",
			wantName:       "app-feature-build", // "日本語" gets replaced with "-" which is trimmed
			wantMaxLen:     63,
		},
		{
			name:           "empty or only invalid characters in revision falls back to rev",
			resourceName:   "app",
			sourceRevision: "///",
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

func TestImageBuildName(t *testing.T) {
	tests := []struct {
		name         string
		resourceName string
		srcRevision  string
		wantName     string
	}{
		{
			name:         "standard input",
			resourceName: "todo-app",
			srcRevision:  "feature/auth-implementation",
			wantName:     "todo-app-feature-auth-implementation",
		},
		{
			name:         "uppercase and dots",
			resourceName: "MyResource",
			srcRevision:  "v1.0.0-Beta.2",
			wantName:     "myresource-v1-0-0-beta-2",
		},
		{
			name:         "truncation to 100 chars",
			resourceName: "this-is-an-extremely-long-resource-name-that-we-are-using-to-test-image-build-custom-resource-length",
			srcRevision:  "some-long-git-branch-name-with-many-words-and-characters-to-trigger-length-truncation-safety-checks",
			wantName:     "this-is-an-extremely-long-resource-name-that-we-are-using-to-test-image-build-custom-resource-length", // truncated to 100 chars
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ImageBuildName(tt.resourceName, tt.srcRevision)
			if len(got) > 100 {
				t.Errorf("ImageBuildName() = %q (len %d), exceeds max 100", got, len(got))
			}
			if got != tt.wantName {
				t.Errorf("ImageBuildName() = %q, want %q", got, tt.wantName)
			}
		})
	}
}
