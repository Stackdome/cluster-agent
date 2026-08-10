#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ruby -ryaml - "$repo_root" <<'RUBY'
repo = ARGV.fetch(0)

def fail_validation(message)
  warn "release provenance validation failed: #{message}"
  exit 1
end

def load_yaml(path)
  YAML.safe_load(File.read(path), aliases: true)
rescue StandardError => error
  fail_validation("cannot parse #{path}: #{error.message}")
end

def triggers(workflow)
  workflow["on"] || workflow[true] || {}
end

def step_index(steps, id: nil, name: nil)
  steps.index do |step|
    (!id || step["id"] == id) && (!name || step["name"] == name)
  end
end

Dir.glob(File.join(repo, "config/docker/*.Dockerfile")).each do |path|
  File.readlines(path, chomp: true).grep(/^FROM\s+/).each do |line|
    image = line.split.find { |field| field.include?("@sha256:") }.to_s
    unless image.match?(/\A[^@\s]+:[^@\s]+@sha256:[0-9a-f]{64}\z/)
      fail_validation("mutable base image in #{path}: #{line}")
    end
  end
end

release_dockerfile = File.read(File.join(repo, "config/docker/release.Dockerfile"))
dockerignore = File.read(File.join(repo, ".dockerignore"))
%w[.git .cache bin charts/stackdome-agent/charts/*.tgz release-artifacts].each do |entry|
  fail_validation(".dockerignore is missing #{entry}") unless dockerignore.lines.map(&:strip).include?(entry)
end
go_version = File.read(File.join(repo, "go.mod"))[/^go ([0-9]+\.[0-9]+\.[0-9]+)$/, 1]
fail_validation("go.mod must declare an exact Go patch version") unless go_version
makefile = File.read(File.join(repo, "Makefile"))
unless makefile.include?("GOLANGCI_LINT_VERSION ?= v2.4.0") &&
       makefile.include?("github.com/golangci/golangci-lint/v2/cmd/golangci-lint") &&
       makefile.include?("GOTOOLCHAIN=$(GO_TOOLCHAIN_VERSION)")
  fail_validation("golangci-lint must use the first maintained release with Go 1.25 support")
end
lint_config = load_yaml(File.join(repo, ".golangci.yml"))
unless lint_config["version"] == "2" &&
       lint_config.dig("issues", "new-from-rev") == "6c7a654224757b3d458ea51dc3887f43eb6b1a2d"
  fail_validation("golangci-lint configuration must use the audited v2 baseline")
end
unless release_dockerfile.lines.first.include?("golang:#{go_version}-")
  fail_validation("release Dockerfile Go version differs from go.mod")
end
%w[cluster-agent-manager containerd-config-reconciler].each do |command|
  fail_validation("release Dockerfile does not build #{command}") unless release_dockerfile.include?(command)
end

standalone_chart = load_yaml(File.join(repo, "charts/stackdome-agent-standalone/Chart.yaml"))
umbrella_chart = load_yaml(File.join(repo, "charts/stackdome-agent/Chart.yaml"))
chart_version = standalone_chart.fetch("version").to_s
unless standalone_chart.fetch("appVersion").to_s == "v#{chart_version}" &&
       umbrella_chart.fetch("version").to_s == chart_version &&
       umbrella_chart.fetch("appVersion").to_s == "v#{chart_version}"
  fail_validation("chart versions differ from the released app version")
end

workflow_paths = Dir.glob(File.join(repo, ".github/workflows/*.y{a,}ml"))
workflow_paths.each do |path|
  File.readlines(path, chomp: true).each do |line|
    next unless line.match?(/^\s*-?\s*uses:/)
    unless line.match?(/uses:\s*[^\s@]+@[0-9a-f]{40}\s+#\s+v?\d/)
      fail_validation("action is not pinned by commit in #{path}: #{line.strip}")
    end
  end
end

ci = load_yaml(File.join(repo, ".github/workflows/ci.yaml"))
ci_triggers = triggers(ci)
unless %w[pull_request push].all? { |event| ci_triggers.key?(event) }
  fail_validation("CI must run on pull requests and default-branch pushes")
end
fail_validation("CI permissions must be contents: read") unless ci["permissions"] == { "contents" => "read" }
ci_jobs = ci.fetch("jobs")
unless ci_jobs.values.all? { |job| job.fetch("timeout-minutes", 0).to_i.positive? }
  fail_validation("every CI job needs a timeout")
end
ci_steps = ci_jobs.values.flat_map { |job| job.fetch("steps") }
ci_checkout = ci_steps.find { |step| step.fetch("uses", "").start_with?("actions/checkout@") } || {}
unless ci_checkout.dig("with", "fetch-depth") == 0 && ci_checkout.dig("with", "persist-credentials") == false
  fail_validation("CI checkout must fetch the lint baseline without credentials")
end
ci_runs = ci_steps.map { |step| step["run"] }.compact.join("\n")
[
  "make test",
  "make lint",
  "git diff --exit-code",
  "./hack/verify-release-assets.sh",
  "./hack/test-release-provenance.sh",
  "make test-integration",
  "config/docker/release.Dockerfile",
  "docker buildx build",
  "--platform linux/amd64,linux/arm64",
  "./hack/validate-oci-index.rb",
].each do |command|
  fail_validation("CI is missing #{command}") unless ci_runs.include?(command)
end
ci_steps.select { |step| step.fetch("uses", "").start_with?("actions/setup-go@") }.each do |step|
  unless step.dig("with", "go-version-file") == "go.mod" && step.dig("with", "check-latest") == false
    fail_validation("CI setup-go must use the exact go.mod toolchain")
  end
end

release = load_yaml(File.join(repo, ".github/workflows/release.yaml"))
release_triggers = triggers(release)
unless release_triggers.keys == ["push"] && release_triggers.dig("push", "tags") == ["v*"]
  fail_validation("release must run only on v* tags")
end
expected_permissions = { "contents" => "read", "id-token" => "write", "attestations" => "write" }
fail_validation("release permissions are broader than required") unless release["permissions"] == expected_permissions
unless release["concurrency"] == { "group" => "cluster-agent-release", "cancel-in-progress" => false }
  fail_validation("release must be serialized without cancellation")
end
expected_destinations = {
  "AGENT_IMAGE" => "quay.io/stackdome/cluster-agent/cluster-agent-manager",
  "RECONCILER_IMAGE" => "quay.io/stackdome/registry-config-reconciler",
  "CHART_REGISTRY" => "oci://quay.io/stackdome/charts",
}
fail_validation("release destinations changed") unless release["env"] == expected_destinations

job = release.dig("jobs", "release") || {}
fail_validation("release job must use agent-release") unless job["environment"] == "agent-release"
fail_validation("release job needs a timeout") unless job.fetch("timeout-minutes", 0).to_i.positive?
unless job.dig("env", "ROLLBACK_CHART_VERSION") == "${{ vars.ROLLBACK_CHART_VERSION }}"
  fail_validation("rollback chart coordinate must come from the protected environment")
end
steps = job.fetch("steps")

checkout = steps.find { |step| step.fetch("uses", "").start_with?("actions/checkout@") } || {}
unless checkout.dig("with", "fetch-depth") == 0 && checkout.dig("with", "persist-credentials") == false
  fail_validation("release checkout must fetch full history without credentials")
end

buildx = steps.find { |step| step.fetch("uses", "").start_with?("docker/setup-buildx-action@") } || {}
unless buildx.dig("with", "driver-opts").to_s.match?(/\Aimage=moby\/buildkit@sha256:[0-9a-f]{64}\z/)
  fail_validation("release BuildKit image must be pinned by digest")
end

trusted = steps.find { |step| step["id"] == "trusted_source" } || {}
trusted_lines = trusted.fetch("run", "").lines.map(&:strip).reject(&:empty?)
expected_trusted_lines = [
  "set -euo pipefail",
  'if [[ -z "$DEFAULT_BRANCH" ]]; then',
  'echo "GitHub did not provide the repository default branch" >&2',
  "exit 1",
  "fi",
  'tagged_commit="$(git rev-parse "${GITHUB_REF}^{commit}")"',
  'test "$(git rev-parse HEAD)" = "$tagged_commit"',
  'git merge-base --is-ancestor "$tagged_commit" "refs/remotes/origin/${DEFAULT_BRANCH}"',
  'echo "commit=$tagged_commit" >>"$GITHUB_OUTPUT"',
]
unless trusted.dig("env", "DEFAULT_BRANCH") == "${{ github.event.repository.default_branch }}" &&
       trusted_lines == expected_trusted_lines
  fail_validation("trusted-source ancestry check is missing")
end

trust_index = step_index(steps, id: "trusted_source")
version_index = step_index(steps, id: "version")
verify_index = step_index(steps, name: "Verify tagged source before using release secrets")
login_index = steps.index { |step| step.fetch("uses", "").start_with?("docker/login-action@") }
trivy_install_index = step_index(steps, name: "Install checksum-verified Trivy before registry login")
unless [trust_index, version_index, verify_index, trivy_install_index, login_index].all? &&
       trust_index < version_index && version_index < verify_index &&
       verify_index < trivy_install_index && trivy_install_index < login_index
  fail_validation("release secrets are available before source trust gates")
end
steps[0...login_index].each do |step|
  fail_validation("secret reference appears before trust gates") if step.to_s.include?("secrets.")
end

release_text = File.read(File.join(repo, ".github/workflows/release.yaml"))
if release_text.include?("aquasecurity/trivy-action") || release_text.include?("aquasecurity/setup-trivy")
  fail_validation("release must not execute Trivy composite actions")
end
trivy_install = steps.fetch(trivy_install_index)
unless trivy_install.fetch("run", "").lines.map(&:strip).reject(&:empty?) == [
  "set -euo pipefail",
  './hack/install-trivy.sh "$RUNNER_TEMP/trivy-bin"',
]
  fail_validation("Trivy must be installed by the checksum-verified repository script")
end
installer = File.read(File.join(repo, "hack/install-trivy.sh"))
unless installer.include?("v${version}/${archive_name}") &&
       installer.include?("2edd39da482bb4e9831962487b68f68e3928ec3137794757f54d00383d79547b") &&
       installer.include?("sha256sum --check")
  fail_validation("Trivy installer is not pinned to the verified release archive")
end
if release_text.match?(/TRIVY_TEST_(?:ARCHIVE|SHA256)|STACKDOME_RELEASE_TOOL_TESTING/)
  fail_validation("release workflow must not enable Trivy installer test overrides")
end

verify_run = steps.fetch(verify_index).fetch("run", "")
[
  "make test",
  "make lint",
  "git diff --exit-code",
  "./hack/test-release-provenance.sh",
  "helm lint charts/stackdome-agent-standalone",
  "helm lint charts/stackdome-agent",
].each do |command|
  fail_validation("release source gate is missing #{command}") unless verify_run.include?(command)
end

builds = {
  "build_agent" => ["${{ env.AGENT_IMAGE }}", "COMMAND=cluster-agent-manager"],
  "build_reconciler" => ["${{ env.RECONCILER_IMAGE }}", "COMMAND=containerd-config-reconciler"],
}
builds.each do |id, (image, command)|
  step = steps.find { |candidate| candidate["id"] == id } || {}
  expected_output = "type=image,name=#{image},push-by-digest=true,name-canonical=true,push=true"
  with = step.fetch("with", {})
  unless step.fetch("uses", "").start_with?("docker/build-push-action@") &&
         with["platforms"] == "linux/amd64,linux/arm64" &&
         with["outputs"] == expected_output &&
         with["file"] == "config/docker/release.Dockerfile" &&
         with.fetch("build-args", "").include?(command) &&
         !with.key?("tags") && !with.key?("push")
    fail_validation("#{id} must publish only an untagged multi-architecture digest")
  end
end

index_step = steps.find { |step| step["name"] == "Verify published image indexes" } || {}
index_lines = index_step.fetch("run", "")
unless index_lines.include?('"${AGENT_IMAGE}@${{ steps.build_agent.outputs.digest }}"') &&
       index_lines.include?('"${RECONCILER_IMAGE}@${{ steps.build_reconciler.outputs.digest }}"') &&
       index_lines.scan(/validate-oci-index\.rb/).length == 2
  fail_validation("release must validate both exact build digests as two-platform indexes")
end

scan_contracts = {
  "vulnerability_scan_agent_amd64" => ["AGENT_IMAGE", "build_agent", "linux/amd64", "release-artifacts/trivy-agent-linux-amd64.json"],
  "vulnerability_scan_agent_arm64" => ["AGENT_IMAGE", "build_agent", "linux/arm64", "release-artifacts/trivy-agent-linux-arm64.json"],
  "vulnerability_scan_reconciler_amd64" => ["RECONCILER_IMAGE", "build_reconciler", "linux/amd64", "release-artifacts/trivy-reconciler-linux-amd64.json"],
  "vulnerability_scan_reconciler_arm64" => ["RECONCILER_IMAGE", "build_reconciler", "linux/arm64", "release-artifacts/trivy-reconciler-linux-arm64.json"],
}
scan_contracts.each do |id, (image_env, build_id, platform, output)|
  step = steps.find { |candidate| candidate["id"] == id } || {}
  expected_lines = [
    %Q{"$RUNNER_TEMP/trivy-bin" image --scanners vuln --platform #{platform} --format json \\},
    %Q{--output #{output} \\},
    %Q{--severity CRITICAL --ignore-unfixed --exit-code 1 \\},
    %Q{"${#{image_env}}@${{ steps.#{build_id}.outputs.digest }}"},
  ]
  unless step["continue-on-error"] == true &&
         step["shell"] == "bash" && !step.key?("uses") &&
         step.fetch("run", "").lines.map(&:strip).reject(&:empty?) == expected_lines
    fail_validation("#{id} does not enforce the public-alpha scan contract")
  end
end

validate_index = step_index(steps, name: "Validate SBOM and vulnerability reports")
gate_index = step_index(steps, name: "Enforce public-alpha vulnerability gate")
promote_agent_index = step_index(steps, id: "promote_agent")
promote_reconciler_index = step_index(steps, id: "promote_reconciler")
package_index = step_index(steps, id: "package_charts")
preflight_index = step_index(steps, id: "publication_preflight")
standalone_chart_index = step_index(steps, id: "chart_standalone")
umbrella_chart_index = step_index(steps, id: "chart_umbrella")
metadata_index = step_index(steps, name: "Bundle CRDs and record immutable release set")
completion_index = step_index(steps, name: "Verify release state and record completion")
unless [validate_index, gate_index, package_index, preflight_index, promote_agent_index,
        promote_reconciler_index, standalone_chart_index, umbrella_chart_index,
        metadata_index, completion_index].all? &&
       validate_index < gate_index && gate_index < package_index && package_index < preflight_index &&
       preflight_index < promote_agent_index && promote_agent_index < promote_reconciler_index &&
       promote_reconciler_index < standalone_chart_index && standalone_chart_index < umbrella_chart_index &&
       umbrella_chart_index < metadata_index && metadata_index < completion_index
  fail_validation("release publication steps are out of order")
end
validation_run = steps.fetch(validate_index).fetch("run", "")
unless validation_run.include?("./hack/validate-release-evidence.rb") &&
       validation_run.include?('--agent-ref "${AGENT_IMAGE}@${{ steps.build_agent.outputs.digest }}"') &&
       validation_run.include?('--reconciler-ref "${RECONCILER_IMAGE}@${{ steps.build_reconciler.outputs.digest }}"')
  fail_validation("release must validate all per-platform SBOM and scan reports")
end

sbom_step = steps.find { |step| step["name"] == "Generate per-platform scanner evidence" } || {}
sbom_lines = sbom_step.fetch("run", "").lines.map(&:strip).reject(&:empty?)
unless sbom_lines == [
  "set -euo pipefail",
  './hack/generate-release-sboms.sh "$RUNNER_TEMP/syft-bin" release-artifacts agent \\',
  '"${AGENT_IMAGE}@${{ steps.build_agent.outputs.digest }}" \\',
  "release-artifacts/agent-index.json",
  './hack/generate-release-sboms.sh "$RUNNER_TEMP/syft-bin" release-artifacts reconciler \\',
  '"${RECONCILER_IMAGE}@${{ steps.build_reconciler.outputs.digest }}" \\',
  "release-artifacts/reconciler-index.json",
]
  fail_validation("scanner evidence generation is not bound to both exact indexes")
end

evidence_upload = steps.find { |step| step["name"] == "Upload vulnerability evidence" } || {}
expected_evidence_paths = [
  "release-artifacts/*.spdx.json",
  "release-artifacts/*.cyclonedx.json",
  "release-artifacts/*.syft.json",
  "release-artifacts/trivy-*.json",
  "release-artifacts/*-index.json",
]
unless evidence_upload.dig("with", "path").to_s.lines.map(&:strip).reject(&:empty?) == expected_evidence_paths
  fail_validation("uploaded vulnerability evidence must retain scanner output and raw indexes")
end

expected_gate = "steps.vulnerability_scan_agent_amd64.outcome != 'success' || steps.vulnerability_scan_agent_arm64.outcome != 'success' || steps.vulnerability_scan_reconciler_amd64.outcome != 'success' || steps.vulnerability_scan_reconciler_arm64.outcome != 'success'"
gate = steps.fetch(gate_index)
unless gate["if"] == expected_gate && gate.fetch("run", "").include?("exit 1")
  fail_validation("public-alpha vulnerability gate condition must be exact")
end

promotion_contracts = {
  "promote_agent" => ["AGENT_IMAGE", "build_agent"],
  "promote_reconciler" => ["RECONCILER_IMAGE", "build_reconciler"],
}
promotion_contracts.each do |id, (repository, build_id)|
  step = steps.find { |candidate| candidate["id"] == id } || {}
  fail_validation("#{id} must stop after any earlier failure") if step.key?("if")
  expected_lines = [
    "set -euo pipefail",
    'promoted="$(./hack/publish-release-coordinate.sh publish-image "$RUNNER_TEMP/crane-bin" \\',
    %Q{"$#{repository}" "${{ steps.version.outputs.version }}" "${{ steps.#{build_id}.outputs.digest }}")"},
    'echo "digest=$promoted" >>"$GITHUB_OUTPUT"',
  ]
  unless step.fetch("run", "").lines.map(&:strip).reject(&:empty?) == expected_lines
    fail_validation("#{id} must publish the exact release version and digest")
  end
end

package_run = steps.fetch(package_index).fetch("run", "")
unless package_run.scan(/go run \.\/cmd\/release-package/).length == 2 &&
       package_run.include?('source-date "${{ steps.version.outputs.source_date }}"') &&
       !package_run.include?("helm push")
  fail_validation("both charts must be packaged deterministically before publication preflight")
end

preflight_run = steps.fetch(preflight_index).fetch("run", "")
expected_preflight_lines = [
  "set -euo pipefail",
  'chart_version="${{ steps.version.outputs.chart_version }}"',
  './hack/publish-release-coordinate.sh check-image "$RUNNER_TEMP/crane-bin" \\',
  '"$AGENT_IMAGE" "${{ steps.version.outputs.version }}" "${{ steps.build_agent.outputs.digest }}"',
  './hack/publish-release-coordinate.sh check-image "$RUNNER_TEMP/crane-bin" \\',
  '"$RECONCILER_IMAGE" "${{ steps.version.outputs.version }}" "${{ steps.build_reconciler.outputs.digest }}"',
  './hack/publish-release-coordinate.sh check-chart helm "$CHART_REGISTRY" \\',
  'stackdome-agent-standalone "$chart_version" \\',
  '"release-artifacts/stackdome-agent-standalone-$chart_version.tgz"',
  './hack/publish-release-coordinate.sh check-chart helm "$CHART_REGISTRY" \\',
  'stackdome-agent "$chart_version" "release-artifacts/stackdome-agent-$chart_version.tgz"',
  './hack/release-state.rb init release-artifacts/publication-state.json',
]
unless preflight_run.lines.map(&:strip).reject(&:empty?) == expected_preflight_lines
  fail_validation("all four immutable coordinates must be preflighted before publication")
end

chart_publication_contracts = {
  "chart_standalone" => [
    "set -euo pipefail",
    'chart_version="${{ steps.version.outputs.chart_version }}"',
    'digest="$(./hack/publish-release-coordinate.sh publish-chart helm "$CHART_REGISTRY" \\',
    'stackdome-agent-standalone "$chart_version" \\',
    '"release-artifacts/stackdome-agent-standalone-$chart_version.tgz")"',
    'echo "digest=$digest" >>"$GITHUB_OUTPUT"',
  ],
  "chart_umbrella" => [
    "set -euo pipefail",
    'chart_version="${{ steps.version.outputs.chart_version }}"',
    'digest="$(./hack/publish-release-coordinate.sh publish-chart helm "$CHART_REGISTRY" \\',
    'stackdome-agent "$chart_version" "release-artifacts/stackdome-agent-$chart_version.tgz")"',
    'echo "digest=$digest" >>"$GITHUB_OUTPUT"',
  ],
}
chart_publication_contracts.each do |id, expected_lines|
  chart_step = steps.find { |candidate| candidate["id"] == id } || {}
  unless !chart_step.key?("if") &&
         chart_step.fetch("run", "").lines.map(&:strip).reject(&:empty?) == expected_lines
    fail_validation("#{id} must publish the exact chart version and deterministic archive")
  end
end

publication_records = {
  "Record agent image publication" => [
    'version="${{ steps.version.outputs.version }}"',
    "./hack/release-state.rb record-publication release-artifacts/publication-state.json \\",
    'agent-image-tag "$AGENT_IMAGE:$version" "${{ steps.promote_agent.outputs.digest }}"',
  ],
  "Record reconciler image publication" => [
    'version="${{ steps.version.outputs.version }}"',
    "./hack/release-state.rb record-publication release-artifacts/publication-state.json \\",
    'reconciler-image-tag "$RECONCILER_IMAGE:$version" \\',
    '"${{ steps.promote_reconciler.outputs.digest }}"',
  ],
  "Record standalone chart publication" => [
    'chart_version="${{ steps.version.outputs.chart_version }}"',
    "./hack/release-state.rb record-publication release-artifacts/publication-state.json \\",
    "standalone-chart-push \\",
    '"quay.io/stackdome/charts/stackdome-agent-standalone:$chart_version" \\',
    '"${{ steps.chart_standalone.outputs.digest }}"',
  ],
  "Record umbrella chart publication" => [
    'chart_version="${{ steps.version.outputs.chart_version }}"',
    "./hack/release-state.rb record-publication release-artifacts/publication-state.json \\",
    'umbrella-chart-push "quay.io/stackdome/charts/stackdome-agent:$chart_version" \\',
    '"${{ steps.chart_umbrella.outputs.digest }}"',
  ],
}
publication_records.each do |step_name, expected_lines|
  run = (steps.find { |step| step["name"] == step_name } || {}).fetch("run", "")
  unless run.lines.map(&:strip).reject(&:empty?) == expected_lines
    fail_validation("#{step_name} must retain its exact tagged OCI coordinate")
  end
end

dependency_builds = Dir.glob([
  File.join(repo, "hack/**/*.sh"),
  File.join(repo, ".github/workflows/*.{yaml,yml}"),
])
  .select { |path| File.file?(path) }
  .reject { |path| %w[validate-release-provenance.sh test-release-provenance.sh].include?(File.basename(path)) }
  .sum { |path| File.read(path).scan(/helm dependency build/).length }
unless dependency_builds == 1 && File.read(File.join(repo, "hack/verify-release-assets.sh")).include?("helm dependency build")
  fail_validation("verified chart dependencies must be fetched exactly once")
end

metadata_run = steps.fetch(metadata_index).fetch("run", "")
%w[source_commit api_versions crd_bundle_sha256 vulnerability_gate scanner_identity rollback_chart rollback_command
   rollback_crd_bundle_sha256 rollback_crd_replace reconciler_digest_required].each do |field|
  fail_validation("release metadata is missing #{field}") unless metadata_run.include?(field)
end
expected_publication_coordinates = [
  '--arg agent_image "$AGENT_IMAGE:${{ steps.version.outputs.version }}@${{ steps.promote_agent.outputs.digest }}"',
  '--arg reconciler_image "$RECONCILER_IMAGE:${{ steps.version.outputs.version }}@${{ steps.promote_reconciler.outputs.digest }}"',
  '--arg standalone_chart "quay.io/stackdome/charts/stackdome-agent-standalone:${{ steps.version.outputs.chart_version }}@${{ steps.chart_standalone.outputs.digest }}"',
  '--arg umbrella_chart "quay.io/stackdome/charts/stackdome-agent:${{ steps.version.outputs.chart_version }}@${{ steps.chart_umbrella.outputs.digest }}"',
]
unless expected_publication_coordinates.all? { |coordinate| metadata_run.include?(coordinate) }
  fail_validation("release metadata must retain all four tagged publication coordinates")
end
unless metadata_run.include?('scanner_identity: "Syft-JSON schema 16.0.39"') &&
       release_text.include?("archive=syft_1.32.0_linux_amd64.tar.gz")
  fail_validation("release metadata must identify the exact pinned scanner evidence schema")
end

[
  "d6400b579fa84dd383573b1d1ff6f081a37fc64d3ffaafdfdda95c4325f204be",
  "c1d593d01551f2c9a3df5ca0a0be4385a839bd9b86d4a76e18d7b17d16559127",
].each do |checksum|
  fail_validation("release tool download lacks checksum #{checksum}") unless release_text.include?(checksum)
end

attestation_contracts = {
  "attest_agent" => ["${{ env.AGENT_IMAGE }}", "${{ steps.promote_agent.outputs.digest }}"],
  "attest_reconciler" => ["${{ env.RECONCILER_IMAGE }}", "${{ steps.promote_reconciler.outputs.digest }}"],
  "attest_chart_standalone" => [
    "quay.io/stackdome/charts/stackdome-agent-standalone",
    "${{ steps.chart_standalone.outputs.digest }}",
  ],
  "attest_chart_umbrella" => [
    "quay.io/stackdome/charts/stackdome-agent",
    "${{ steps.chart_umbrella.outputs.digest }}",
  ],
}
attestations = attestation_contracts.map do |id, (subject_name, subject_digest)|
  attestation = steps.find { |step| step["id"] == id } || {}
  expected_inputs = {
    "subject-name" => subject_name,
    "subject-digest" => subject_digest,
    "push-to-registry" => true,
  }
  unless attestation.fetch("uses", "").start_with?("actions/attest-build-provenance@") &&
         attestation["with"] == expected_inputs
    fail_validation("#{id} must attest its exact published OCI subject")
  end
  attestation
end

record_contracts = {
  "Record agent attestation" => ["attest_agent", "agent-attestation", 'agent-attestation "$AGENT_IMAGE"', "promote_agent", "agent.json", '$AGENT_IMAGE'],
  "Record reconciler attestation" => ["attest_reconciler", "reconciler-attestation", 'reconciler-attestation "$RECONCILER_IMAGE"', "promote_reconciler", "reconciler.json", '$RECONCILER_IMAGE'],
  "Record standalone chart attestation" => ["attest_chart_standalone", "standalone-chart-attestation", 'subject="quay.io/stackdome/charts/stackdome-agent-standalone"', "chart_standalone", "standalone-chart.json", '$subject'],
  "Record umbrella chart attestation" => ["attest_chart_umbrella", "umbrella-chart-attestation", 'subject="quay.io/stackdome/charts/stackdome-agent"', "chart_umbrella", "umbrella-chart.json", '$subject'],
}
record_contracts.each do |name, (attestation_id, checkpoint, subject, digest_step, bundle, repository)|
  record = steps.find { |step| step["name"] == name } || {}
  run = record.fetch("run", "")
  required = [
    "release-state.rb record-attestation",
    checkpoint,
    subject,
    "steps.#{digest_step}.outputs.digest",
    "steps.#{attestation_id}.outputs.attestation-id",
    "steps.#{attestation_id}.outputs.bundle-path",
    "release-artifacts/attestations/#{bundle}",
    "gh attestation verify",
    '--repo "$GITHUB_REPOSITORY"',
    "--bundle release-artifacts/attestations/#{bundle}",
    "--bundle-from-oci",
  ]
  expected_ref = %Q{"oci://#{repository}@${{ steps.#{digest_step}.outputs.digest }}"}
  unless required.all? { |value| run.include?(value) } &&
         run.scan(/"oci:\/\/[^"]+"/) == [expected_ref, expected_ref]
    fail_validation("#{name} must retain and verify exact attestation evidence")
  end
end

completion_run = steps.fetch(completion_index).fetch("run", "")
unless completion_run.include?("release-state.rb complete release-artifacts/publication-state.json") &&
       completion_run.include?("release-artifacts/release-metadata.json") &&
       completion_run.include?("release-artifacts/release-complete") &&
       completion_run.include?("release-artifacts/SHA256SUMS") &&
       attestations.all? { |attestation| steps.index(attestation) < completion_index }
  fail_validation("release completion must verify all recorded publication and attestation evidence")
end

publisher = File.read(File.join(repo, "hack/publish-release-coordinate.sh"))
unless publisher.include?("use a new version") && publisher.include?('existing" != "$expected_digest') &&
       publisher.include?('actual_sha" != "$expected_sha')
  fail_validation("publication helper must reject mismatched immutable coordinates")
end

rollback_step = steps.find { |step| step["id"] == "rollback" } || {}
rollback_run = rollback_step.fetch("run", "")
unless rollback_run.include?("extract-chart-crds.sh") && rollback_run.include?("verify-crd-compatibility.rb") &&
       rollback_run.include?("release-artifacts/rollback-crds.yaml")
  fail_validation("rollback chart CRDs must be captured and compatibility-gated")
end
release_docs = File.read(File.join(repo, "RELEASING.md"))
unless release_docs.include?("verify-installed-crds.rb") && release_docs.include?("controller scaled to zero") &&
       release_docs.include?("prepare-crd-rollback.rb") && release_docs.include?("release-complete")
  fail_validation("release documentation is missing partial-publication or explicit CRD recovery")
end
unless release_docs.include?("./hack/test-release-recovery.sh") &&
       !release_docs.include?("./hack/test-release-recovery.rb")
  fail_validation("release documentation must invoke the current recovery test")
end
backup_contract = [
  'YAML.load_stream(File.read(ARGV.fetch(0))).compact.each do |crd|',
  'plural = crd.dig("spec", "names", "plural")',
  'rollback-crds.yaml >protected-crd-resources.tsv',
  'kubectl get "$resource" --all-namespaces -o yaml',
  'done <protected-crd-resources.tsv',
  'sha256sum --check pre-rollback-backups.sha256',
  'restorable PostgreSQL backup for every `PostgresCluster`',
  'provider snapshots or equivalent backups',
  'Kubernetes custom-resource exports do not contain',
]
unless backup_contract.all? { |text| release_docs.include?(text) } &&
       release_docs.index("sha256sum --check pre-rollback-backups.sha256") <
         release_docs.index("kubectl replace -f rollback-replacements.yaml")
  fail_validation("CRD rollback must verify every protected resource and data-plane backup before mutation")
end
if release_docs.include?("kubectl get clusterregistries,stacks,stackresources,imagebuilds")
  fail_validation("CRD rollback must not use a partial hard-coded resource list")
end

puts "release provenance contract verified"
RUBY
