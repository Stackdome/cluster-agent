package config

import (
	"fmt"
	"regexp"
	"strings"
)

var digestImageReference = regexp.MustCompile(`^[^@[:space:]]+@sha256:[0-9a-f]{64}$`)

const (
	// ZotImage must be >= v2.1.8. Earlier versions (e.g. v2.1.2) have a GC bug:
	// removeUntaggedManifests() only handles OCI media types and skips Docker
	// schema-2 (docker2s2) manifests. Kaniko pushes build-cache layers as Docker
	// schema-2, so untagged cache manifests are never garbage collected and their
	// blobs are never reclaimed, growing the registry PVC without bound until the
	// node hits disk-pressure eviction. v2.1.8 added compat.IsCompatibleManifestMediaType
	// to that path.
	ZotImage                      = "ghcr.io/project-zot/zot-linux-amd64:v2.1.18"
	RegistryConfigReconcilerImage = "quay.io/stackdome/registry-config-reconciler:v0.6.12-alpha-rc1"
	NfsServerImage                = "adnanhodzic/nfs-server-k8s:0.1"
	// v1.28.0
	// Fork of kaniko.
	KanikoExecutorImage     = "ghcr.io/osscontainertools/kaniko:v1.28.0"
	StackdomeToolsImage     = "quay.io/stackdome/tools:v0.0.1"
	StackdomeSSHServerImage = "quay.io/stackdome/ssh-server:v.0.0.1"
	GitSyncImage            = "registry.k8s.io/git-sync/git-sync:v4.7.0"
	RustFSImage             = "rustfs/rustfs@sha256:7c72df8e6705aa5eb18f5fb2d9252103e897afd1533a5658935fe70929eda5ce"
)

// ValidateRegistryConfigReconcilerImage validates the image reference used by
// the privileged host-config reconciler. Cloud installations set
// requireDigest so the deployed DaemonSet cannot follow a mutable tag.
func ValidateRegistryConfigReconcilerImage(image string, requireDigest bool) error {
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("registry config reconciler image is required")
	}

	isDigestReference := digestImageReference.MatchString(image)
	repository := strings.SplitN(image, "@", 2)[0]
	lastPathSegment := repository[strings.LastIndex(repository, "/")+1:]
	if strings.Contains(lastPathSegment, ":") && strings.Contains(image, "@") {
		isDigestReference = false
	}
	if strings.Contains(image, "@") && !isDigestReference {
		return fmt.Errorf("registry config reconciler image has an invalid digest reference: %q", image)
	}
	if requireDigest && !isDigestReference {
		return fmt.Errorf("registry config reconciler image must use an immutable sha256 digest: %q", image)
	}

	return nil
}
