#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

agent_digest="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
reconciler_digest="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
agent_ref="quay.io/stackdome/cluster-agent/cluster-agent-manager@$agent_digest"
reconciler_ref="quay.io/stackdome/registry-config-reconciler@$reconciler_digest"

new_fixture() {
  local fixture="$1"
  mkdir -p "$fixture"
  ruby -rjson - "$fixture" "$agent_ref" "$reconciler_ref" <<'RUBY'
dir, agent_ref, reconciler_ref = ARGV
{"agent" => agent_ref, "reconciler" => reconciler_ref}.each do |name, ref|
  %w[amd64 arm64].each do |arch|
    platform = "linux/#{arch}"
    File.write(File.join(dir, "trivy-#{name}-linux-#{arch}.json"), JSON.pretty_generate({
      "SchemaVersion" => 2,
      "ArtifactName" => ref,
      "Results" => [],
      "Metadata" => {
        "RepoDigests" => [ref],
        "ImageConfig" => {"architecture" => arch, "os" => "linux"},
      },
    }))
    File.write(File.join(dir, "#{name}-linux-#{arch}.spdx.json"), JSON.pretty_generate({
      "spdxVersion" => "SPDX-2.3",
      "annotations" => [
        {"comment" => "stackdome:image-reference=#{ref}"},
        {"comment" => "stackdome:platform=#{platform}"},
      ],
    }))
    File.write(File.join(dir, "#{name}-linux-#{arch}.cyclonedx.json"), JSON.pretty_generate({
      "bomFormat" => "CycloneDX",
      "metadata" => {"component" => {"properties" => [
        {"name" => "stackdome:image-reference", "value" => ref},
        {"name" => "stackdome:platform", "value" => platform},
      ]}},
    }))
  end
end
RUBY
}

validate() {
  "$repo_root/hack/validate-release-evidence.rb" \
    --artifact-dir "$1" \
    --agent-ref "$agent_ref" \
    --reconciler-ref "$reconciler_ref"
}

expect_mutation_failure() {
  local name="$1"
  local ruby_mutation="$2"
  local fixture="$tmp/$name"
  new_fixture "$fixture"
  ruby -rjson - "$fixture" "$ruby_mutation" <<'RUBY'
dir, mutation = ARGV
path, expression = mutation.split("|", 2)
file = File.join(dir, path)
document = JSON.parse(File.read(file))
eval(expression)
File.write(file, JSON.pretty_generate(document))
RUBY
  if validate "$fixture" >/dev/null 2>&1; then
    echo "release evidence mutation unexpectedly passed: $name" >&2
    exit 1
  fi
}

new_fixture "$tmp/valid"
validate "$tmp/valid"

expect_mutation_failure trivy-artifact \
  'trivy-agent-linux-amd64.json|document["ArtifactName"] = "alpine:latest"'
expect_mutation_failure trivy-repo-digest \
  'trivy-agent-linux-amd64.json|document["Metadata"]["RepoDigests"] = ["quay.io/example/other@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"]'
expect_mutation_failure trivy-os \
  'trivy-agent-linux-amd64.json|document["Metadata"]["ImageConfig"]["os"] = "windows"'
expect_mutation_failure trivy-architecture \
  'trivy-agent-linux-amd64.json|document["Metadata"]["ImageConfig"]["architecture"] = "arm64"'
expect_mutation_failure spdx-reference \
  'agent-linux-amd64.spdx.json|document["annotations"][0]["comment"] = "stackdome:image-reference=alpine:latest"'
expect_mutation_failure spdx-platform \
  'agent-linux-amd64.spdx.json|document["annotations"][1]["comment"] = "stackdome:platform=linux/arm64"'
expect_mutation_failure cyclonedx-reference \
  'agent-linux-amd64.cyclonedx.json|document["metadata"]["component"]["properties"][0]["value"] = "alpine:latest"'
expect_mutation_failure cyclonedx-platform \
  'agent-linux-amd64.cyclonedx.json|document["metadata"]["component"]["properties"][1]["value"] = "linux/arm64"'

echo "release evidence negative tests passed"
