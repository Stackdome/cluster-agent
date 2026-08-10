#!/usr/bin/env bash
set -euo pipefail

syft="${1:?usage: generate-release-sboms.sh <syft> <artifact-dir> <name> <image@digest>}"
artifact_dir="${2:?}"
name="${3:?}"
image="${4:?}"

[[ "$name" == "agent" || "$name" == "reconciler" ]]
[[ "$image" =~ ^[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]]
mkdir -p "$artifact_dir"

for arch in amd64 arm64; do
  platform="linux/$arch"
  spdx="$artifact_dir/$name-linux-$arch.spdx.json"
  cyclonedx="$artifact_dir/$name-linux-$arch.cyclonedx.json"
  "$syft" "registry:$image" \
    --platform "$platform" \
    --output "spdx-json=$spdx" \
    --output "cyclonedx-json=$cyclonedx"

  jq --arg image "$image" --arg platform "$platform" '
    .annotations = ((.annotations // []) + [
      {
        "annotationDate": (.creationInfo.created // "1970-01-01T00:00:00Z"),
        "annotationType": "OTHER",
        "annotator": "Tool: stackdome-release",
        "comment": ("stackdome:image-reference=" + $image)
      },
      {
        "annotationDate": (.creationInfo.created // "1970-01-01T00:00:00Z"),
        "annotationType": "OTHER",
        "annotator": "Tool: stackdome-release",
        "comment": ("stackdome:platform=" + $platform)
      }
    ])' "$spdx" >"$spdx.tmp"
  mv "$spdx.tmp" "$spdx"

  jq --arg image "$image" --arg platform "$platform" '
    .metadata.component.properties = ((.metadata.component.properties // []) + [
      {"name": "stackdome:image-reference", "value": $image},
      {"name": "stackdome:platform", "value": $platform}
    ])' "$cyclonedx" >"$cyclonedx.tmp"
  mv "$cyclonedx.tmp" "$cyclonedx"
done
