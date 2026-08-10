#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

write_index() {
  local platforms="$1"
  ruby -rjson - "$tmp/index.json" "$platforms" <<'RUBY'
path, platforms = ARGV
manifests = platforms.split(",").map do |platform|
  os, architecture = platform.split("/", 2)
  {"mediaType" => "application/vnd.oci.image.manifest.v1+json", "platform" => {"os" => os, "architecture" => architecture}}
end
File.write(path, JSON.generate({"schemaVersion" => 2, "mediaType" => "application/vnd.oci.image.index.v1+json", "manifests" => manifests}))
RUBY
}

write_index 'linux/amd64,linux/arm64'
"$repo_root/hack/validate-oci-index.rb" "$tmp/index.json"

for platforms in 'linux/amd64' 'linux/amd64,linux/amd64' 'linux/amd64,windows/arm64'; do
  write_index "$platforms"
  if "$repo_root/hack/validate-oci-index.rb" "$tmp/index.json" >/dev/null 2>&1; then
    echo "invalid OCI platform set unexpectedly passed: $platforms" >&2
    exit 1
  fi
done

echo "OCI image index negative tests passed"
