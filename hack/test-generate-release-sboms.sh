#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

amd64_manifest="sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
arm64_manifest="sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
ruby -rjson - "$tmp/index.json" "$amd64_manifest" "$arm64_manifest" <<'RUBY'
path, amd64_digest, arm64_digest = ARGV
index = {
  "schemaVersion" => 2,
  "mediaType" => "application/vnd.oci.image.index.v1+json",
  "manifests" => [
    {"mediaType" => "application/vnd.oci.image.manifest.v1+json", "digest" => amd64_digest,
     "platform" => {"os" => "linux", "architecture" => "amd64"}},
    {"mediaType" => "application/vnd.oci.image.manifest.v1+json", "digest" => arm64_digest,
     "platform" => {"os" => "linux", "architecture" => "arm64"}},
  ],
}
File.binwrite(path, JSON.generate(index))
RUBY
index_digest="sha256:$(sha256sum "$tmp/index.json" | awk '{print $1}')"

cat >"$tmp/syft" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 9 ]]
[[ "$1" == "registry:$EXPECTED_IMAGE" ]]
[[ "$2" == "--platform" ]]
[[ "$3" =~ ^linux/(amd64|arm64)$ ]]
[[ "$4" == "--output" ]]
[[ "$6" == "--output" ]]
[[ "$8" == "--output" ]]

repository="${EXPECTED_IMAGE%@*}"
index_digest="${EXPECTED_IMAGE##*@}"
arch="${3##*/}"
case "$arch" in
  amd64) platform_manifest="$AMD64_MANIFEST" ;;
  arm64) platform_manifest="$ARM64_MANIFEST" ;;
esac
escaped_repository="${repository//\//%2F}"
escaped_manifest="${platform_manifest//:/%3A}"
[[ "$5" == "spdx-json=$EXPECTED_ARTIFACT_DIR/agent-linux-$arch.spdx.json" ]]
[[ "$7" == "cyclonedx-json=$EXPECTED_ARTIFACT_DIR/agent-linux-$arch.cyclonedx.json" ]]
[[ "$9" == "syft-json=$EXPECTED_ARTIFACT_DIR/agent-linux-$arch.syft.json" ]]

# These roots match Syft 1.32.0 output: SPDX carries a platform-manifest purl,
# CycloneDX does not carry architecture, and Syft JSON retains the full source identity.
jq -n --arg repository "$repository" --arg digest "$index_digest" \
  --arg purl "pkg:oci/$escaped_repository@$escaped_manifest?arch=$arch" '{
    spdxVersion: "SPDX-2.3",
    name: $repository,
    creationInfo: {created: "2026-01-01T00:00:00Z"},
    relationships: [{
      spdxElementId: "SPDXRef-DOCUMENT",
      relatedSpdxElement: "SPDXRef-Root-Container",
      relationshipType: "DESCRIBES"
    }],
    packages: [{
      SPDXID: "SPDXRef-Root-Container",
      name: $repository,
      versionInfo: $digest,
      primaryPackagePurpose: "CONTAINER",
      externalRefs: [{
        referenceCategory: "PACKAGE-MANAGER",
        referenceType: "purl",
        referenceLocator: $purl
      }]
    }]
  }' >"${5#spdx-json=}"

jq -n --arg repository "$repository" --arg digest "$index_digest" '{
    bomFormat: "CycloneDX",
    specVersion: "1.6",
    metadata: {
      tools: {components: [{type: "application", author: "anchore", name: "syft", version: "1.32.0"}]},
      component: {type: "container", name: $repository, version: $digest}
    }
  }' >"${7#cyclonedx-json=}"

jq -n --arg repository "$repository" --arg image "$EXPECTED_IMAGE" \
  --arg digest "$index_digest" --arg manifest "$platform_manifest" --arg arch "$arch" '{
    schema: {version: "16.0.39"},
    source: {
      id: ($manifest | sub("^sha256:"; "")),
      name: $repository,
      version: $digest,
      type: "image",
      metadata: {
        userInput: $image,
        manifestDigest: $manifest,
        mediaType: "application/vnd.oci.image.manifest.v1+json",
        architecture: $arch,
        os: "linux"
      }
    },
    descriptor: {name: "syft", version: "1.32.0"}
  }' >"${9#syft-json=}"
SH
chmod +x "$tmp/syft"

image="quay.io/stackdome/cluster-agent/cluster-agent-manager@$index_digest"
EXPECTED_IMAGE="$image" EXPECTED_ARTIFACT_DIR="$tmp/evidence" \
  AMD64_MANIFEST="$amd64_manifest" ARM64_MANIFEST="$arm64_manifest" \
  "$repo_root/hack/generate-release-sboms.sh" \
  "$tmp/syft" "$tmp/evidence" agent "$image" "$tmp/index.json"

for arch in amd64 arm64; do
  test -s "$tmp/evidence/agent-linux-$arch.spdx.json"
  test -s "$tmp/evidence/agent-linux-$arch.cyclonedx.json"
  test -s "$tmp/evidence/agent-linux-$arch.syft.json"
  jq --exit-status '.annotations == null' "$tmp/evidence/agent-linux-$arch.spdx.json" >/dev/null
  jq --exit-status '.metadata.component.purl == null and .metadata.component.properties == null' \
    "$tmp/evidence/agent-linux-$arch.cyclonedx.json" >/dev/null
  jq --exit-status --arg arch "$arch" '.source.metadata.architecture == $arch' \
    "$tmp/evidence/agent-linux-$arch.syft.json" >/dev/null
done

bad_digest="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
if EXPECTED_IMAGE="${image%@*}@$bad_digest" EXPECTED_ARTIFACT_DIR="$tmp/bad" \
  AMD64_MANIFEST="$amd64_manifest" ARM64_MANIFEST="$arm64_manifest" \
  "$repo_root/hack/generate-release-sboms.sh" \
  "$tmp/syft" "$tmp/bad" agent "${image%@*}@$bad_digest" "$tmp/index.json" >/dev/null 2>&1; then
  echo "SBOM generation accepted an index file for another digest" >&2
  exit 1
fi

echo "release SBOM generator preserves and binds scanner-native output"
