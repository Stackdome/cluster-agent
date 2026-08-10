#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/syft" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
for argument in "$@"; do
  case "$argument" in
    spdx-json=*) printf '{"spdxVersion":"SPDX-2.3","creationInfo":{"created":"2026-01-01T00:00:00Z"}}' >"${argument#spdx-json=}" ;;
    cyclonedx-json=*) printf '{"bomFormat":"CycloneDX","metadata":{"component":{}}}' >"${argument#cyclonedx-json=}" ;;
  esac
done
SH
chmod +x "$tmp/syft"

digest="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
image="quay.io/stackdome/cluster-agent/cluster-agent-manager@$digest"
"$repo_root/hack/generate-release-sboms.sh" "$tmp/syft" "$tmp/evidence" agent "$image"

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
done

echo "release SBOM generator binding tests passed"
