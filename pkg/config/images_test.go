package config

import "testing"

func TestValidateRegistryConfigReconcilerImage(t *testing.T) {
	tests := []struct {
		name          string
		image         string
		requireDigest bool
		wantError     bool
	}{
		{
			name:          "cloud accepts digest reference",
			image:         "quay.io/stackdome/registry-config-reconciler@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			requireDigest: true,
		},
		{
			name:          "cloud rejects tag reference",
			image:         "quay.io/stackdome/registry-config-reconciler:v0.6.12-alpha-rc1",
			requireDigest: true,
			wantError:     true,
		},
		{
			name:  "self hosted accepts tag reference",
			image: "quay.io/stackdome/registry-config-reconciler:v0.6.12-alpha-rc1",
		},
		{
			name:      "invalid digest is always rejected",
			image:     "quay.io/stackdome/registry-config-reconciler@sha256:short",
			wantError: true,
		},
		{
			name:          "cloud rejects tag plus digest reference",
			image:         "quay.io/stackdome/registry-config-reconciler:v1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			requireDigest: true,
			wantError:     true,
		},
		{
			name:      "empty image is rejected",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRegistryConfigReconcilerImage(tt.image, tt.requireDigest)
			if (err != nil) != tt.wantError {
				t.Fatalf("ValidateRegistryConfigReconcilerImage() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}
