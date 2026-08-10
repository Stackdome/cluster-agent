#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
syft="${1:?usage: generate-release-sboms.sh <syft> <artifact-dir> <name> <image@digest> <index.json>}"
artifact_dir="${2:?}"
name="${3:?}"
image="${4:?}"
index="${5:?}"

[[ "$name" == "agent" || "$name" == "reconciler" ]]
[[ "$image" =~ ^[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]]
[[ -s "$index" ]]

expected_index_digest="${image##*@}"
actual_index_digest="sha256:$(sha256sum "$index" | awk '{print $1}')"
if [[ "$actual_index_digest" != "$expected_index_digest" ]]; then
  echo "$index has digest $actual_index_digest, expected $expected_index_digest" >&2
  exit 1
fi
"$script_dir/validate-oci-index.rb" "$index" >/dev/null

mkdir -p "$artifact_dir"
for arch in amd64 arm64; do
  platform="linux/$arch"
  spdx="$artifact_dir/$name-linux-$arch.spdx.json"
  cyclonedx="$artifact_dir/$name-linux-$arch.cyclonedx.json"
  syft_json="$artifact_dir/$name-linux-$arch.syft.json"
  "$syft" "registry:$image" \
    --platform "$platform" \
    --output "spdx-json=$spdx" \
    --output "cyclonedx-json=$cyclonedx" \
    --output "syft-json=$syft_json"
  for output in "$spdx" "$cyclonedx" "$syft_json"; do
    if [[ ! -s "$output" ]]; then
      echo "Syft did not produce $output" >&2
      exit 1
    fi
  done
done
