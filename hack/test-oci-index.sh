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
  digest_character = architecture == "amd64" ? "a" : "b"
  {"mediaType" => "application/vnd.oci.image.manifest.v1+json",
   "digest" => "sha256:#{digest_character * 64}",
   "platform" => {"os" => os, "architecture" => architecture}}
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

write_index 'linux/amd64,linux/arm64'
ruby -rjson - "$tmp/index.json" <<'RUBY'
path = ARGV.fetch(0)
index = JSON.parse(File.read(path))
index["manifests"][0]["digest"] = "sha256:not-a-digest"
File.write(path, JSON.generate(index))
RUBY
if "$repo_root/hack/validate-oci-index.rb" "$tmp/index.json" >/dev/null 2>&1; then
  echo "OCI index with a malformed platform digest unexpectedly passed" >&2
  exit 1
fi

write_index 'linux/amd64,linux/arm64'
ruby -rjson - "$tmp/index.json" <<'RUBY'
path = ARGV.fetch(0)
index = JSON.parse(File.read(path))
index["manifests"][0]["mediaType"] = "application/octet-stream"
File.write(path, JSON.generate(index))
RUBY
if "$repo_root/hack/validate-oci-index.rb" "$tmp/index.json" >/dev/null 2>&1; then
  echo "OCI index with an invalid platform media type unexpectedly passed" >&2
  exit 1
fi

echo "OCI image index negative tests passed"
