#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/syft" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 7 ]]
[[ "$1" == "registry:$EXPECTED_IMAGE" ]]
[[ "$2" == "--platform" ]]
[[ "$3" =~ ^linux/(amd64|arm64)$ ]]
[[ "$4" == "--output" ]]
[[ "$6" == "--output" ]]

repository="${EXPECTED_IMAGE%@*}"
digest="${EXPECTED_IMAGE##*@}"
arch="${3##*/}"
escaped_repository="${repository//\//%2F}"
[[ "$5" == "spdx-json=$EXPECTED_ARTIFACT_DIR/agent-linux-$arch.spdx.json" ]]
[[ "$7" == "cyclonedx-json=$EXPECTED_ARTIFACT_DIR/agent-linux-$arch.cyclonedx.json" ]]

jq -n --arg repository "$repository" --arg digest "$digest" --arg arch "$arch" \
  --arg purl "pkg:oci/$escaped_repository@$digest?arch=$arch" '{
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

jq -n --arg repository "$repository" --arg digest "$digest" '{
    bomFormat: "CycloneDX",
    metadata: {component: {
      type: "container",
      name: $repository,
      version: $digest
    }}
  }' >"${7#cyclonedx-json=}"
SH
chmod +x "$tmp/syft"

digest="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
image="quay.io/stackdome/cluster-agent/cluster-agent-manager@$digest"
EXPECTED_IMAGE="$image" EXPECTED_ARTIFACT_DIR="$tmp/evidence" \
  "$repo_root/hack/generate-release-sboms.sh" \
  "$tmp/syft" "$tmp/evidence" agent "$image"

for arch in amd64 arm64; do
  jq --exit-status --arg image "$image" --arg platform "linux/$arch" '
    ([.annotations[].comment] | index("stackdome:image-reference=" + $image)) != null and
    ([.annotations[].comment] | index("stackdome:platform=" + $platform)) != null
  ' "$tmp/evidence/agent-linux-$arch.spdx.json" >/dev/null
  jq --exit-status --arg image "$image" --arg platform "linux/$arch" '
    [.metadata.component.properties[]] == [
      {"name":"stackdome:image-reference","value":$image},
      {"name":"stackdome:platform","value":$platform}
    ]
  ' "$tmp/evidence/agent-linux-$arch.cyclonedx.json" >/dev/null
  jq --exit-status --arg arch "$arch" '
    .metadata.component.purl | endswith("?arch=" + $arch)
  ' "$tmp/evidence/agent-linux-$arch.cyclonedx.json" >/dev/null
done

echo "release SBOM generator binding tests passed"
