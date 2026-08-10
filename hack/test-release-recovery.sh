#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"

cat >"$tmp/bin/crane" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  digest)
    key="$(printf '%s' "$2" | tr '/:' '__')"
    [[ -f "$REMOTE_STATE/images/$key" ]] || { echo MANIFEST_UNKNOWN >&2; exit 1; }
    cat "$REMOTE_STATE/images/$key"
    ;;
  tag)
    digest="${2##*@}"; repository="${2%@*}"
    key="$(printf '%s' "$repository:$3" | tr '/:' '__')"
    mkdir -p "$REMOTE_STATE/images"
    printf '%s\n' "$digest" >"$REMOTE_STATE/images/$key"
    ;;
esac
SH

cat >"$tmp/bin/helm" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  pull)
    ref="$2"; version="$4"; destination="$6"; name="${ref##*/}"
    key="$(printf '%s' "$name:$version" | tr '/:' '__')"
    [[ -f "$REMOTE_STATE/charts/$key.tgz" ]] || { echo MANIFEST_UNKNOWN >&2; exit 1; }
    cp "$REMOTE_STATE/charts/$key.tgz" "$destination/$name-$version.tgz"
    printf 'Digest: %s\n' "$(cat "$REMOTE_STATE/charts/$key.digest")"
    ;;
  push)
    archive="$2"; base="$(basename "$archive" .tgz)"
    version="$CHART_VERSION"; name="${base%-$version}"
    key="$(printf '%s' "$name:$version" | tr '/:' '__')"
    mkdir -p "$REMOTE_STATE/charts"
    cp "$archive" "$REMOTE_STATE/charts/$key.tgz"
    digest="sha256:$(sha256sum "$archive" | awk '{print $1}')"
    printf '%s\n' "$digest" >"$REMOTE_STATE/charts/$key.digest"
    printf 'Digest: %s\n' "$digest"
    ;;
esac
SH

cat >"$tmp/bin/attest" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
subject="$1"; digest="$2"; bundle="$3"
key="$(printf '%s@%s' "$subject" "$digest" | shasum -a 256 | awk '{print $1}')"
mkdir -p "$REMOTE_STATE/attestations"
printf '%s\n' "$subject@$digest" >"$REMOTE_STATE/attestations/$key"
ruby -rjson -rbase64 - "$subject" "$digest" "$bundle" <<'RUBY'
subject, digest, destination = ARGV
algorithm, value = digest.split(":", 2)
statement = {
  "_type" => "https://in-toto.io/Statement/v1",
  "subject" => [{"name" => subject, "digest" => {algorithm => value}}],
  "predicateType" => "https://slsa.dev/provenance/v1",
  "predicate" => {},
}
bundle = {
  "mediaType" => "application/vnd.dev.sigstore.bundle.v0.3+json",
  "dsseEnvelope" => {"payload" => Base64.strict_encode64(JSON.generate(statement)), "signatures" => []},
}
File.write(destination, JSON.generate(bundle) + "\n")
RUBY
printf 'attestation-%s\n' "$key"
SH
chmod +x "$tmp/bin/"*

publisher="$repo_root/hack/publish-release-coordinate.sh"
state_helper="$repo_root/hack/release-state.rb"
version="v0.6.12-alpha-rc1"
chart_version="0.6.12-alpha-rc1"
export CHART_VERSION="$chart_version"
agent_name="quay.io/stackdome/cluster-agent/cluster-agent-manager"
reconciler_name="quay.io/stackdome/registry-config-reconciler"
standalone_name="quay.io/stackdome/charts/stackdome-agent-standalone:$chart_version"
umbrella_name="quay.io/stackdome/charts/stackdome-agent:$chart_version"
agent_digest="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
reconciler_digest="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

checkpoint() {
  completed=$((completed + 1))
  if [[ "$fail_after" == "$completed" ]]; then
    return 42
  fi
}

run_release() {
  local artifacts="$1"
  fail_after="$2"
  completed=0
  rm -rf "$artifacts"
  mkdir -p "$artifacts/attestations"
  printf 'standalone chart bytes' >"$artifacts/stackdome-agent-standalone-$chart_version.tgz"
  printf 'umbrella chart bytes' >"$artifacts/stackdome-agent-$chart_version.tgz"
  "$state_helper" init "$artifacts/publication-state.json"

  "$publisher" check-image "$tmp/bin/crane" "$agent_name" "$version" "$agent_digest" >/dev/null
  "$publisher" check-image "$tmp/bin/crane" "$reconciler_name" "$version" "$reconciler_digest" >/dev/null
  "$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
    stackdome-agent-standalone "$chart_version" "$artifacts/stackdome-agent-standalone-$chart_version.tgz" >/dev/null
  "$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
    stackdome-agent "$chart_version" "$artifacts/stackdome-agent-$chart_version.tgz" >/dev/null

  digest="$("$publisher" publish-image "$tmp/bin/crane" "$agent_name" "$version" "$agent_digest")"
  "$state_helper" record-publication "$artifacts/publication-state.json" agent-image-tag "$agent_name" "$digest"
  checkpoint || return $?

  digest="$("$publisher" publish-image "$tmp/bin/crane" "$reconciler_name" "$version" "$reconciler_digest")"
  "$state_helper" record-publication "$artifacts/publication-state.json" reconciler-image-tag "$reconciler_name" "$digest"
  checkpoint || return $?

  standalone_digest="$("$publisher" publish-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
    stackdome-agent-standalone "$chart_version" "$artifacts/stackdome-agent-standalone-$chart_version.tgz" 2>/dev/null)"
  "$state_helper" record-publication "$artifacts/publication-state.json" standalone-chart-push "$standalone_name" "$standalone_digest"
  checkpoint || return $?

  umbrella_digest="$("$publisher" publish-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
    stackdome-agent "$chart_version" "$artifacts/stackdome-agent-$chart_version.tgz" 2>/dev/null)"
  "$state_helper" record-publication "$artifacts/publication-state.json" umbrella-chart-push "$umbrella_name" "$umbrella_digest"
  checkpoint || return $?

  jq -n \
    --arg agent "$agent_name@$agent_digest" \
    --arg reconciler "$reconciler_name@$reconciler_digest" \
    --arg standalone "$standalone_name@$standalone_digest" \
    --arg umbrella "$umbrella_name@$umbrella_digest" \
    '{images:{agent:$agent,reconciler:$reconciler},charts:{standalone:$standalone,umbrella:$umbrella}}' \
    >"$artifacts/release-metadata.json"

  for row in \
    "agent-attestation|$agent_name|$agent_digest|agent.json" \
    "reconciler-attestation|$reconciler_name|$reconciler_digest|reconciler.json" \
    "standalone-chart-attestation|$standalone_name|$standalone_digest|standalone-chart.json" \
    "umbrella-chart-attestation|$umbrella_name|$umbrella_digest|umbrella-chart.json"; do
    IFS='|' read -r name subject subject_digest bundle_name <<<"$row"
    source_bundle="$artifacts/source-$bundle_name"
    attestation_id="$("$tmp/bin/attest" "$subject" "$subject_digest" "$source_bundle")"
    "$state_helper" record-attestation "$artifacts/publication-state.json" "$name" \
      "$subject" "$subject_digest" "$attestation_id" "$source_bundle" \
      "$artifacts/attestations/$bundle_name"
    rm "$source_bundle"
    checkpoint || return $?
  done

  "$state_helper" complete "$artifacts/publication-state.json" \
    "$artifacts/release-metadata.json" "$artifacts" \
    "$artifacts/release-complete" "$artifacts/SHA256SUMS"
}

for failure_index in $(seq 1 8); do
  export REMOTE_STATE="$tmp/remote-$failure_index"
  mkdir -p "$REMOTE_STATE"
  if run_release "$tmp/attempt-$failure_index-a" "$failure_index"; then
    echo "failure injection after checkpoint $failure_index unexpectedly completed" >&2
    exit 1
  else
    failure_status=$?
  fi
  [[ "$failure_status" == 42 ]]
  [[ ! -e "$tmp/attempt-$failure_index-a/release-complete" ]]

  run_release "$tmp/attempt-$failure_index-b" 0
  [[ -f "$tmp/attempt-$failure_index-b/release-complete" ]]
  [[ "$(jq '.checkpoints | length' "$tmp/attempt-$failure_index-b/publication-state.json")" == 8 ]]
  [[ "$(find "$REMOTE_STATE/images" -type f | wc -l | tr -d ' ')" == 2 ]]
  [[ "$(find "$REMOTE_STATE/charts" -name '*.tgz' | wc -l | tr -d ' ')" == 2 ]]
  [[ "$(find "$REMOTE_STATE/attestations" -type f | wc -l | tr -d ' ')" == 4 ]]
  (cd "$tmp/attempt-$failure_index-b" && sha256sum --check SHA256SUMS >/dev/null)
done

# Completion is the last write: a checksum failure must not leave a marker.
last_artifacts="$tmp/attempt-8-b"
printf 'not a directory' >"$last_artifacts/checksum-parent-is-a-file"
if "$state_helper" complete "$last_artifacts/publication-state.json" \
  "$last_artifacts/release-metadata.json" "$last_artifacts" \
  "$last_artifacts/failed-release-complete" \
  "$last_artifacts/checksum-parent-is-a-file/SHA256SUMS" >/dev/null 2>&1; then
  echo "invalid checksum destination unexpectedly completed" >&2
  exit 1
fi
[[ ! -e "$last_artifacts/failed-release-complete" ]]

jq '(.checkpoints[] | select(.name == "agent-attestation")) |= del(.attestation_id)' \
  "$last_artifacts/publication-state.json" >"$last_artifacts/state-without-attestation-id.json"
if "$state_helper" complete "$last_artifacts/state-without-attestation-id.json" \
  "$last_artifacts/release-metadata.json" "$last_artifacts" \
  "$last_artifacts/missing-id-release-complete" "$last_artifacts/missing-id-SHA256SUMS" \
  >/dev/null 2>&1; then
  echo "state without an attestation ID unexpectedly completed" >&2
  exit 1
fi
[[ ! -e "$last_artifacts/missing-id-release-complete" ]]

# The state boundary must reject an attestation bundle for another subject.
export REMOTE_STATE="$tmp/remote-negative"
mkdir -p "$REMOTE_STATE"
"$state_helper" init "$tmp/negative-state.json"
"$tmp/bin/attest" "$reconciler_name" "$reconciler_digest" "$tmp/wrong-bundle.json" >/dev/null
if "$state_helper" record-attestation "$tmp/negative-state.json" agent-attestation \
  "$agent_name" "$agent_digest" attestation-wrong "$tmp/wrong-bundle.json" "$tmp/retained.json" >/dev/null 2>&1; then
  echo "wrong attestation subject unexpectedly passed state recording" >&2
  exit 1
fi

"$tmp/bin/attest" "$agent_name" "$agent_digest" "$tmp/correct-bundle.json" >/dev/null
"$state_helper" record-attestation "$tmp/negative-state.json" agent-attestation \
  "$agent_name" "$agent_digest" attestation-original "$tmp/correct-bundle.json" "$tmp/retained.json"
retained_checksum="$(sha256sum "$tmp/retained.json" | awk '{print $1}')"
if "$state_helper" record-attestation "$tmp/negative-state.json" agent-attestation \
  "$agent_name" "$agent_digest" attestation-conflict "$tmp/correct-bundle.json" "$tmp/retained.json" >/dev/null 2>&1; then
  echo "conflicting attestation checkpoint unexpectedly passed" >&2
  exit 1
fi
[[ "$(sha256sum "$tmp/retained.json" | awk '{print $1}')" == "$retained_checksum" ]]

echo "release interruption recovery exercised every production checkpoint"
