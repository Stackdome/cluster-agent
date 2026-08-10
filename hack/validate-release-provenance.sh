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
ci_runs = ci_steps.map { |step| step["run"] }.compact.join("\n")
[
  "make test",
  "git diff --exit-code",
  "./hack/verify-release-assets.sh",
  "./hack/test-release-provenance.sh",
  "make test-integration",
  "config/docker/release.Dockerfile",
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
unless [trust_index, version_index, verify_index, login_index].all? &&
       trust_index < version_index && version_index < verify_index && verify_index < login_index
  fail_validation("release secrets are available before source trust gates")
end
steps[0...login_index].each do |step|
  fail_validation("secret reference appears before trust gates") if step.to_s.include?("secrets.")
end

verify_run = steps.fetch(verify_index).fetch("run", "")
[
  "make test",
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

scan_ids = %w[
  vulnerability_scan_agent_amd64
  vulnerability_scan_agent_arm64
  vulnerability_scan_reconciler_amd64
  vulnerability_scan_reconciler_arm64
]
scan_ids.each do |id|
  step = steps.find { |candidate| candidate["id"] == id } || {}
  with = step.fetch("with", {})
  unless step["continue-on-error"] == true &&
         step.fetch("uses", "").start_with?("aquasecurity/trivy-action@") &&
         with["version"] == "v0.64.1" && with["scan-type"] == "image" &&
         with["format"] == "json" && with["severity"] == "CRITICAL" &&
         with["ignore-unfixed"] == true && with["exit-code"] == 1 && with["cache"] == false &&
         step.dig("env", "TRIVY_PLATFORM") == "linux/#{id.end_with?("amd64") ? "amd64" : "arm64"}"
    fail_validation("#{id} does not enforce the public-alpha scan contract")
  end
end

validate_index = step_index(steps, name: "Validate SBOM and vulnerability reports")
gate_index = step_index(steps, name: "Enforce public-alpha vulnerability gate")
promote_agent_index = step_index(steps, id: "promote_agent")
promote_reconciler_index = step_index(steps, id: "promote_reconciler")
charts_index = step_index(steps, id: "charts")
metadata_index = step_index(steps, name: "Bundle CRDs and record immutable release set")
unless [validate_index, gate_index, promote_agent_index, promote_reconciler_index, charts_index, metadata_index].all? &&
       validate_index < gate_index && gate_index < promote_agent_index && gate_index < promote_reconciler_index &&
       promote_agent_index < charts_index && promote_reconciler_index < charts_index && charts_index < metadata_index
  fail_validation("release publication steps are out of order")
end
validation_run = steps.fetch(validate_index).fetch("run", "")
unless validation_run.include?("*.spdx.json") && validation_run.include?("*.cyclonedx.json") &&
       validation_run.include?("trivy-*.json") && validation_run.scan(/-eq 4/).length == 3
  fail_validation("release must validate all per-platform SBOM and scan reports")
end

expected_gate = "steps.vulnerability_scan_agent_amd64.outcome != 'success' || steps.vulnerability_scan_agent_arm64.outcome != 'success' || steps.vulnerability_scan_reconciler_amd64.outcome != 'success' || steps.vulnerability_scan_reconciler_arm64.outcome != 'success'"
gate = steps.fetch(gate_index)
unless gate["if"] == expected_gate && gate.fetch("run", "").include?("exit 1")
  fail_validation("public-alpha vulnerability gate condition must be exact")
end

%w[promote_agent promote_reconciler].each do |id|
  step = steps.find { |candidate| candidate["id"] == id }
  fail_validation("#{id} must stop after any earlier failure") if step.key?("if")
  run = step.fetch("run", "")
  unless run.include?("crane-bin\" tag") && run.include?('test "$promoted" = "$digest"')
    fail_validation("#{id} must verify final-tag digest equality")
  end
end

charts = steps.fetch(charts_index)
charts_run = charts.fetch("run", "")
unless charts_run.scan(/go run \.\/cmd\/release-package/).length == 2 &&
       charts_run.include?('source-date "${{ steps.version.outputs.source_date }}"') &&
       charts_run.scan(/helm push/).length == 2 &&
       charts_run.include?("standalone_digest") && charts_run.include?("umbrella_digest")
  fail_validation("both charts must publish with captured OCI digests")
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
%w[source_commit api_versions crd_bundle_sha256 vulnerability_gate rollback_chart rollback_command].each do |field|
  fail_validation("release metadata is missing #{field}") unless metadata_run.include?(field)
end

release_text = File.read(File.join(repo, ".github/workflows/release.yaml"))
[
  "d6400b579fa84dd383573b1d1ff6f081a37fc64d3ffaafdfdda95c4325f204be",
  "c1d593d01551f2c9a3df5ca0a0be4385a839bd9b86d4a76e18d7b17d16559127",
].each do |checksum|
  fail_validation("release tool download lacks checksum #{checksum}") unless release_text.include?(checksum)
end

attestations = steps.select { |step| step.fetch("uses", "").start_with?("actions/attest-build-provenance@") }
fail_validation("both images and both charts need GitHub provenance") unless attestations.length == 4

puts "release provenance contract verified"
RUBY
