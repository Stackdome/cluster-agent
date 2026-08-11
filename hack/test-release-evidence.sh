#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

agent_amd64_manifest="sha256:1111111111111111111111111111111111111111111111111111111111111111"
agent_arm64_manifest="sha256:2222222222222222222222222222222222222222222222222222222222222222"
reconciler_amd64_manifest="sha256:3333333333333333333333333333333333333333333333333333333333333333"
reconciler_arm64_manifest="sha256:4444444444444444444444444444444444444444444444444444444444444444"

write_index() {
  local destination="$1" amd64_digest="$2" arm64_digest="$3"
  ruby -rjson - "$destination" "$amd64_digest" "$arm64_digest" <<'RUBY'
path, amd64_digest, arm64_digest = ARGV
File.binwrite(path, JSON.generate({
  "schemaVersion" => 2,
  "mediaType" => "application/vnd.oci.image.index.v1+json",
  "manifests" => [
    {"mediaType" => "application/vnd.oci.image.manifest.v1+json", "digest" => amd64_digest,
     "platform" => {"os" => "linux", "architecture" => "amd64"}},
    {"mediaType" => "application/vnd.oci.image.manifest.v1+json", "digest" => arm64_digest,
     "platform" => {"os" => "linux", "architecture" => "arm64"}},
  ],
}))
RUBY
}

write_index "$tmp/agent-index.json" "$agent_amd64_manifest" "$agent_arm64_manifest"
write_index "$tmp/reconciler-index.json" "$reconciler_amd64_manifest" "$reconciler_arm64_manifest"
agent_digest="sha256:$(sha256sum "$tmp/agent-index.json" | awk '{print $1}')"
reconciler_digest="sha256:$(sha256sum "$tmp/reconciler-index.json" | awk '{print $1}')"
agent_ref="quay.io/stackdome/cluster-agent/cluster-agent-manager@$agent_digest"
reconciler_ref="quay.io/stackdome/registry-config-reconciler@$reconciler_digest"

new_fixture() {
  local fixture="$1"
  mkdir -p "$fixture"
  cp "$tmp/agent-index.json" "$tmp/reconciler-index.json" "$fixture/"
  ruby -rjson - "$fixture" "$agent_ref" "$reconciler_ref" \
    "$agent_amd64_manifest" "$agent_arm64_manifest" \
    "$reconciler_amd64_manifest" "$reconciler_arm64_manifest" <<'RUBY'
dir, agent_ref, reconciler_ref, agent_amd64, agent_arm64, reconciler_amd64, reconciler_arm64 = ARGV
images = {
  "agent" => [agent_ref, {"amd64" => agent_amd64, "arm64" => agent_arm64}],
  "reconciler" => [reconciler_ref, {"amd64" => reconciler_amd64, "arm64" => reconciler_arm64}],
}
images.each do |name, (ref, manifests)|
  repository, index_digest = ref.split("@", 2)
  %w[amd64 arm64].each do |arch|
    platform_manifest = manifests.fetch(arch)
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
      "name" => repository,
      "documentNamespace" => "https://anchore.example/syft/#{name}-#{arch}",
      "relationships" => [{
        "spdxElementId" => "SPDXRef-DOCUMENT",
        "relatedSpdxElement" => "SPDXRef-Root-Container",
        "relationshipType" => "DESCRIBES",
      }],
      "packages" => [{
        "SPDXID" => "SPDXRef-Root-Container",
        "name" => repository,
        "versionInfo" => index_digest,
        "primaryPackagePurpose" => "CONTAINER",
        "externalRefs" => [{
          "referenceCategory" => "PACKAGE-MANAGER",
          "referenceType" => "purl",
          "referenceLocator" => "pkg:oci/#{repository.gsub('/', '%2F')}@#{platform_manifest.gsub(':', '%3A')}?arch=#{arch}",
        }],
      }],
    }))
    File.write(File.join(dir, "#{name}-linux-#{arch}.cyclonedx.json"), JSON.pretty_generate({
      "bomFormat" => "CycloneDX",
      "specVersion" => "1.6",
      "serialNumber" => "urn:uuid:00000000-0000-0000-0000-#{name == 'agent' ? '000000000001' : '000000000002'}",
      "metadata" => {
        "tools" => {"components" => [{
          "type" => "application", "author" => "anchore", "name" => "syft", "version" => "1.32.0",
        }]},
        "component" => {"type" => "container", "name" => repository, "version" => index_digest},
      },
    }))
    File.write(File.join(dir, "#{name}-linux-#{arch}.syft.json"), JSON.pretty_generate({
      "schema" => {"version" => "16.0.39"},
      "source" => {
        "id" => platform_manifest.delete_prefix("sha256:"),
        "name" => repository,
        "version" => index_digest,
        "type" => "image",
        "metadata" => {
          "userInput" => ref,
          "manifestDigest" => platform_manifest,
          "mediaType" => "application/vnd.oci.image.manifest.v1+json",
          "architecture" => arch,
          "os" => "linux",
        },
      },
      "descriptor" => {"name" => "syft", "version" => "1.32.0"},
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

expect_mutation_failure index-platform-manifest \
  'agent-index.json|document["manifests"][0]["digest"] = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_mutation_failure index-platform-media-type \
  'agent-index.json|document["manifests"][0]["mediaType"] = "application/octet-stream"'
expect_mutation_failure index-extra-platform \
  'agent-index.json|document["manifests"] << {"mediaType" => "application/vnd.oci.image.manifest.v1+json", "digest" => "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "platform" => {"os" => "linux", "architecture" => "s390x"}}'
expect_mutation_failure trivy-artifact \
  'trivy-agent-linux-amd64.json|document["ArtifactName"] = "alpine:latest"'
expect_mutation_failure trivy-repo-digest \
  'trivy-agent-linux-amd64.json|document["Metadata"]["RepoDigests"] = ["quay.io/example/other@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"]'
expect_mutation_failure trivy-os \
  'trivy-agent-linux-amd64.json|document["Metadata"]["ImageConfig"]["os"] = "windows"'
expect_mutation_failure trivy-architecture \
  'trivy-agent-linux-amd64.json|document["Metadata"]["ImageConfig"]["architecture"] = "arm64"'
expect_mutation_failure spdx-native-subject \
  'agent-linux-amd64.spdx.json|document["packages"][0]["name"] = "docker.io/library/alpine"'
expect_mutation_failure spdx-native-index-digest \
  'agent-linux-amd64.spdx.json|document["packages"][0]["versionInfo"] = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"'
expect_mutation_failure spdx-native-platform-digest \
  'agent-linux-amd64.spdx.json|document["packages"][0]["externalRefs"][0]["referenceLocator"] = document["packages"][0]["externalRefs"][0]["referenceLocator"].sub(/@sha256%3A[0-9a-f]{64}/, "@sha256%3Aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")'
expect_mutation_failure spdx-native-architecture \
  'agent-linux-amd64.spdx.json|document["packages"][0]["externalRefs"][0]["referenceLocator"] = document["packages"][0]["externalRefs"][0]["referenceLocator"].sub("arch=amd64", "arch=arm64")'
expect_mutation_failure cyclonedx-native-subject \
  'agent-linux-amd64.cyclonedx.json|document["metadata"]["component"]["name"] = "docker.io/library/alpine"'
expect_mutation_failure cyclonedx-native-index-digest \
  'agent-linux-amd64.cyclonedx.json|document["metadata"]["component"]["version"] = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"'
expect_mutation_failure cyclonedx-injected-platform-claim \
  'agent-linux-amd64.cyclonedx.json|document["metadata"]["component"]["purl"] = "pkg:oci/quay.io%2Fstackdome%2Fcluster-agent%2Fcluster-agent-manager@sha256%3Aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?arch=amd64"'
expect_mutation_failure syft-native-subject \
  'agent-linux-amd64.syft.json|document["source"]["name"] = "docker.io/library/alpine"'
expect_mutation_failure syft-native-index-digest \
  'agent-linux-amd64.syft.json|document["source"]["version"] = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_mutation_failure syft-native-platform-digest \
  'agent-linux-amd64.syft.json|document["source"]["metadata"]["manifestDigest"] = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
expect_mutation_failure syft-native-architecture \
  'agent-linux-amd64.syft.json|document["source"]["metadata"]["architecture"] = "arm64"'
expect_mutation_failure syft-native-platform \
  'agent-linux-amd64.syft.json|document["source"]["metadata"]["os"] = "windows"'
expect_mutation_failure syft-native-input \
  'agent-linux-amd64.syft.json|document["source"]["metadata"]["userInput"] = "alpine:latest"'
expect_mutation_failure syft-scanner-version \
  'agent-linux-amd64.syft.json|document["descriptor"]["version"] = "1.31.0"'

echo "release evidence negative tests cover native index and platform identity"
