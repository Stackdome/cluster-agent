#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

new_fixture() {
  local fixture
  fixture="$(mktemp -d)"
  mkdir -p "$fixture/.github/workflows" "$fixture/config/docker" \
    "$fixture/charts/stackdome-agent-standalone" "$fixture/charts/stackdome-agent" "$fixture/hack"
  cp "$repo_root/go.mod" "$repo_root/.dockerignore" "$fixture/"
  cp "$repo_root/.github/workflows/"*.yaml "$fixture/.github/workflows/"
  cp "$repo_root/config/docker/"*.Dockerfile "$fixture/config/docker/"
  cp "$repo_root/charts/stackdome-agent-standalone/Chart.yaml" "$fixture/charts/stackdome-agent-standalone/"
  cp "$repo_root/charts/stackdome-agent/Chart.yaml" "$fixture/charts/stackdome-agent/"
  cp "$repo_root/hack/validate-release-provenance.sh" \
    "$repo_root/hack/install-trivy.sh" \
    "$repo_root/hack/verify-release-assets.sh" \
    "$fixture/hack/"
  printf '%s\n' "$fixture"
}

expect_failure() {
  local fixture="$1"
  local expected="$2"
  local output="$fixture/output"
  if "$fixture/hack/validate-release-provenance.sh" >"$output" 2>&1; then
    echo "provenance mutation unexpectedly passed: $expected" >&2
    exit 1
  fi
  if ! grep -Fq "$expected" "$output"; then
    cat "$output" >&2
    echo "mutation failed for the wrong reason; expected: $expected" >&2
    exit 1
  fi
}

"$repo_root/hack/validate-release-provenance.sh"
"$repo_root/hack/test-install-trivy.sh"
"$repo_root/hack/test-release-evidence.sh"
"$repo_root/hack/test-generate-release-sboms.sh"
"$repo_root/hack/test-release-publication.sh"
"$repo_root/hack/test-release-recovery.rb"
"$repo_root/hack/test-crd-compatibility.sh"
"$repo_root/hack/test-oci-index.sh"

fixture="$(new_fixture)"
sed -i.bak '1s/@sha256:[0-9a-f]*//' "$fixture/config/docker/release.Dockerfile"
expect_failure "$fixture" "mutable base image"

fixture="$(new_fixture)"
sed -i.bak 's/actions\/checkout@[0-9a-f]*/actions\/checkout@v4/' "$fixture/.github/workflows/ci.yaml"
expect_failure "$fixture" "action is not pinned by commit"

fixture="$(new_fixture)"
sed -i.bak 's/moby\/buildkit@sha256:[0-9a-f]*/moby\/buildkit:buildx-stable-1/' "$fixture/.github/workflows/release.yaml"
expect_failure "$fixture" "release BuildKit image must be pinned by digest"

fixture="$(new_fixture)"
sed -i.bak '/^  push:$/,/^    branches:/d' "$fixture/.github/workflows/ci.yaml"
expect_failure "$fixture" "CI must run on pull requests and default-branch pushes"

fixture="$(new_fixture)"
sed -i.bak '/^    environment: agent-release$/d' "$fixture/.github/workflows/release.yaml"
expect_failure "$fixture" "release job must use agent-release"

fixture="$(new_fixture)"
sed -i.bak 's/git merge-base --is-ancestor/true # git merge-base --is-ancestor/' "$fixture/.github/workflows/release.yaml"
expect_failure "$fixture" "trusted-source ancestry check is missing"

fixture="$(new_fixture)"
sed -i.bak "/^        if: steps.vulnerability_scan_agent_amd64/ s/$/ \&\& false/" "$fixture/.github/workflows/release.yaml"
expect_failure "$fixture" "public-alpha vulnerability gate condition must be exact"

mutate_scan() {
  local old="$1"
  local new="$2"
  local fixture
  fixture="$(new_fixture)"
  OLD="$old" NEW="$new" ruby - "$fixture/.github/workflows/release.yaml" <<'RUBY'
path = ARGV.fetch(0)
text = File.read(path)
pattern = /(^      - name: Scan agent linux\/amd64\n.*?)(?=^      - name:)/m
block = text[pattern]
abort "agent amd64 scanner block not found" unless block
abort "scanner mutation target not found" unless block.sub!(ENV.fetch("OLD"), ENV.fetch("NEW"))
text.sub!(pattern, block)
File.write(path, text)
RUBY
  expect_failure "$fixture" "vulnerability_scan_agent_amd64 does not enforce"
}

expected_agent_ref='"${AGENT_IMAGE}@${{ steps.build_agent.outputs.digest }}"'
mutate_scan "$expected_agent_ref" '"${AGENT_IMAGE}:latest"'
mutate_scan "$expected_agent_ref" '"${AGENT_IMAGE}@${{ steps.build_reconciler.outputs.digest }}"'
mutate_scan "$expected_agent_ref" '"quay.io/example/unrelated@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"'
mutate_scan "$expected_agent_ref" '"${AGENT_IMAGE}@${{ github.sha }}"'
mutate_scan '"$RUNNER_TEMP/trivy-bin" image --scanners vuln --platform linux/amd64 --format json' \
  '"$RUNNER_TEMP/trivy-bin" image --scanners vuln --platform linux/arm64 --format json'
mutate_scan '--output release-artifacts/trivy-agent-linux-amd64.json' \
  '--output release-artifacts/trivy-agent-linux-arm64.json'

fixture="$(new_fixture)"
sed -i.bak 's/2edd39da482bb4e9831962487b68f68e3928ec3137794757f54d00383d79547b/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/' \
  "$fixture/hack/install-trivy.sh"
expect_failure "$fixture" "Trivy installer is not pinned"

fixture="$(new_fixture)"
ruby - "$fixture/.github/workflows/release.yaml" <<'RUBY'
path = ARGV.fetch(0)
text = File.read(path)
marker = "      - name: Install checksum-verified Trivy before registry login\n"
abort "Trivy install marker not found" unless text.sub!(marker, marker + "        uses: aquasecurity/trivy-action@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa # v1\n")
File.write(path, text)
RUBY
expect_failure "$fixture" "release must not execute Trivy composite actions"

fixture="$(new_fixture)"
ruby - "$fixture/.github/workflows/release.yaml" <<'RUBY'
path = ARGV.fetch(0)
text = File.read(path)
block = text.slice!(/^      - name: Promote verified agent digest\n.*?(?=^      - name: Promote verified reconciler digest)/m)
abort "promotion block not found" unless block
marker = "      - name: Validate SBOM and vulnerability reports\n"
abort "validation marker not found" unless text.sub!(marker, block + marker)
File.write(path, text)
RUBY
expect_failure "$fixture" "release publication steps are out of order"

fixture="$(new_fixture)"
ruby - "$fixture/.github/workflows/release.yaml" <<'RUBY'
path = ARGV.fetch(0)
text = File.read(path)
marker = "        id: promote_agent\n"
abort "promotion marker not found" unless text.sub!(marker, marker + "        if: always()\n")
File.write(path, text)
RUBY
expect_failure "$fixture" "promote_agent must stop after any earlier failure"

fixture="$(new_fixture)"
ruby - "$fixture/.github/workflows/release.yaml" <<'RUBY'
path = ARGV.fetch(0)
text = File.read(path)
marker = "          outputs: type=image,name=${{ env.AGENT_IMAGE }},push-by-digest=true,name-canonical=true,push=true\n"
abort "agent output marker not found" unless text.sub!(marker, marker + "          tags: unsafe:latest\n")
File.write(path, text)
RUBY
expect_failure "$fixture" "build_agent must publish only an untagged multi-architecture digest"

fixture="$(new_fixture)"
sed -i.bak 's/appVersion: "v0.6.12-alpha-rc1"/appVersion: "v0.6.11-alpha-rc6"/' "$fixture/charts/stackdome-agent-standalone/Chart.yaml"
expect_failure "$fixture" "chart versions differ from the released app version"

fixture="$(new_fixture)"
sed -i.bak 's/crd_bundle_sha256/crd_checksum/g' "$fixture/.github/workflows/release.yaml"
expect_failure "$fixture" "release metadata is missing crd_bundle_sha256"

echo "release provenance mutation tests passed"
