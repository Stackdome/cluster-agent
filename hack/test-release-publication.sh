#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/state"

cat >"$tmp/bin/crane" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  digest)
    [[ "$#" == 2 ]] || { printf 'unexpected crane digest arguments: %s\n' "$*" >&2; exit 64; }
    if [[ -n "${CLIENT_ERROR:-}" ]]; then printf '%s\n' "$CLIENT_ERROR" >&2; exit 1; fi
    ref="$2"
    repository="${ref%:*}"
    version="${ref##*:}"
    registry="${repository%%/*}"
    repository_path="${repository#*/}"
    key="$(printf '%s' "$2" | tr '/:' '__')"
    if [[ ! -f "$STATE/$key" ]]; then
      printf 'Error: GET https://%s/v2/%s/manifests/%s: MANIFEST_UNKNOWN: manifest unknown; map[]\n' \
        "$registry" "$repository_path" "$version" >&2
      exit 1
    fi
    if [[ -n "${CRANE_DIGEST_OUTPUT:-}" ]]; then printf '%s\n' "$CRANE_DIGEST_OUTPUT"; exit 0; fi
    cat "$STATE/$key"
    ;;
  tag)
    [[ "$#" == 3 ]] || { printf 'unexpected crane tag arguments: %s\n' "$*" >&2; exit 64; }
    digest="${2##*@}"
    repository="${2%@*}"
    key="$(printf '%s' "$repository:$3" | tr '/:' '__')"
    printf '%s\n' "$digest" >"$STATE/$key"
    ;;
esac
SH

cat >"$tmp/bin/helm" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  pull)
    [[ "$#" == 6 && "$3" == --version && "$5" == --destination ]] || {
      printf 'unexpected helm pull arguments: %s\n' "$*" >&2
      exit 64
    }
    ref="$2"; version="$4"; destination="$6"; name="${ref##*/}"
    if [[ -n "${EXPECTED_HELM_VERSION:-}" && "$version" != "$EXPECTED_HELM_VERSION" ]]; then
      printf 'helm pull version was %s, expected %s\n' "$version" "$EXPECTED_HELM_VERSION" >&2
      exit 64
    fi
    coordinate="${ref#oci://}"
    registry="${coordinate%%/*}"
    repository="${coordinate#*/}"
    manifest_version="${version//+/_}"
    key="$(printf '%s' "$name:$version" | tr '/:' '__')"
    if [[ -n "${CLIENT_ERROR:-}" ]]; then printf '%s\n' "$CLIENT_ERROR" >&2; exit 1; fi
    if [[ ! -f "$STATE/$key.tgz" ]]; then
      printf 'Error: failed to perform "FetchReference" on source: %s/%s:%s: not found\n' \
        "$registry" "$repository" "$manifest_version" >&2
      exit 1
    fi
    cp "$STATE/$key.tgz" "$destination/$name-$version.tgz"
    case "${HELM_PULL_DIGEST_MODE:-valid}" in
      valid) printf 'Digest: %s\n' "$(cat "$STATE/$key.digest")" ;;
      missing) printf 'Pulled: %s\n' "$ref" ;;
      multiple) printf 'Digest: %s\nDigest: %s\n' "$(cat "$STATE/$key.digest")" "$(cat "$STATE/$key.digest")" ;;
      malformed) printf 'Digest: sha256:not-a-digest\n' ;;
    esac
    ;;
  push)
    [[ "$#" == 3 ]] || { printf 'unexpected helm push arguments: %s\n' "$*" >&2; exit 64; }
    archive="$2"; registry="$3"; base="$(basename "$archive" .tgz)"
    name="${base%-*}"; version="${base##*-}"
    key="$(printf '%s' "$name:$version" | tr '/:' '__')"
    cp "$archive" "$STATE/$key.tgz"
    digest="sha256:$(sha256sum "$archive" | awk '{print $1}')"
    stored_digest="${HELM_STORED_DIGEST:-$digest}"
    printf '%s\n' "$stored_digest" >"$STATE/$key.digest"
    case "${HELM_PUSH_DIGEST_MODE:-valid}" in
      valid) printf 'Digest: %s\n' "$digest" ;;
      missing) printf 'Pushed: %s\n' "$registry" ;;
      multiple) printf 'Digest: %s\nDigest: %s\n' "$digest" "$digest" ;;
      malformed) printf 'Digest: sha256:not-a-digest\n' ;;
    esac
    ;;
esac
SH
chmod +x "$tmp/bin/crane" "$tmp/bin/helm"

export STATE="$tmp/state"
publisher="$repo_root/hack/publish-release-coordinate.sh"
image_digest="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

expect_failure() {
  local description="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    echo "$description unexpectedly passed" >&2
    exit 1
  fi
}

for error in \
  'UNAUTHORIZED: authentication required' \
  'TOOMANYREQUESTS: rate limit exceeded' \
  'x509: certificate signed by unknown authority' \
  'dial tcp: network is unreachable' \
  'docker-credential-pass: executable file not found in PATH' \
  '404 Not Found' \
  'client returned malformed output' \
  'UNAUTHORIZED: authentication required; MANIFEST_UNKNOWN'; do
  expect_failure "image client error '$error'" env CLIENT_ERROR="$error" \
    "$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/absent v1 "$image_digest"
done

crane_absence='Error: GET https://quay.io/v2/stackdome/absent/manifests/v1: MANIFEST_UNKNOWN: manifest unknown; map[]'
for error in \
  "$crane_absence; unexpected EOF" \
  "$crane_absence; context deadline exceeded" \
  "$crane_absence; read: connection reset by peer" \
  "$crane_absence; lookup quay.io: no such host" \
  "$crane_absence; unexpected end of JSON input" \
  "$crane_absence"$'\nMANIFEST_UNKNOWN: partial response'; do
  expect_failure "mixed image absence response '$error'" env CLIENT_ERROR="$error" \
    "$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/absent v1 "$image_digest"
done

CLIENT_ERROR=$'2026/08/10 12:34:56 HEAD request failed, falling back on GET: HEAD https://quay.io/v2/stackdome/absent/manifests/v1: unexpected status code 404 Not Found (HEAD responses have no body, use GET for details)\nError: GET https://quay.io/v2/stackdome/absent/manifests/v1: MANIFEST_UNKNOWN: manifest unknown; map[]' \
  "$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/absent v1 "$image_digest"
CLIENT_ERROR='Error: GET https://quay.io/v2/stackdome/absent/manifests/v1: NAME_UNKNOWN: repository name not known to registry; map[]' \
  "$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/absent v1 "$image_digest"
expect_failure "Crane HEAD and GET responses for different coordinates" env \
  CLIENT_ERROR=$'2026/08/10 12:34:56 HEAD request failed, falling back on GET: HEAD https://quay.io/v2/stackdome/other/manifests/v1: unexpected status code 404 Not Found (HEAD responses have no body, use GET for details)\nError: GET https://quay.io/v2/stackdome/absent/manifests/v1: MANIFEST_UNKNOWN: manifest unknown; map[]' \
  "$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/absent v1 "$image_digest"
for error in \
  'Error: GET https://quay.io/v2/stackdome/other/manifests/v1: MANIFEST_UNKNOWN: manifest unknown; map[]' \
  'Error: GET https://quay.io/v2/stackdome/absent-extra/manifests/v1: MANIFEST_UNKNOWN: manifest unknown; map[]' \
  'Error: GET https://quay.io/v2/stackdome/absent/manifests/v10: MANIFEST_UNKNOWN: manifest unknown; map[]' \
  'Error: GET https://QUAY.io/v2/stackdome/absent/manifests/v1: MANIFEST_UNKNOWN: manifest unknown; map[]' \
  'Error: GET https://quay.io/v2/stackdome/absent/manifests/v%31: MANIFEST_UNKNOWN: manifest unknown; map[]'; do
  expect_failure "image absence for another coordinate '$error'" env CLIENT_ERROR="$error" \
    "$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/absent v1 "$image_digest"
done

"$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/agent v1 "$image_digest"
test "$("$publisher" publish-image "$tmp/bin/crane" quay.io/stackdome/agent v1 "$image_digest")" = "$image_digest"
expect_failure "malformed image digest" env CRANE_DIGEST_OUTPUT=not-a-digest \
  "$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/agent v1 "$image_digest"
expect_failure "multiple image digests" env CRANE_DIGEST_OUTPUT="$image_digest
$image_digest" "$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/agent v1 "$image_digest"
test "$("$publisher" publish-image "$tmp/bin/crane" quay.io/stackdome/agent v1 "$image_digest")" = "$image_digest"
if "$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/agent v1 \
  sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb >/dev/null 2>&1; then
  echo "mismatched existing image coordinate unexpectedly passed" >&2
  exit 1
fi

printf 'deterministic chart bytes' >"$tmp/stackdome-agent-1.0.0.tgz"
for error in \
  'UNAUTHORIZED: authentication required' \
  'TOOMANYREQUESTS: rate limit exceeded' \
  'x509: certificate signed by unknown authority' \
  'dial tcp: network is unreachable' \
  'docker-credential-pass: executable file not found in PATH' \
  '404 Not Found' \
  'client returned malformed output' \
  'UNAUTHORIZED: authentication required; MANIFEST_UNKNOWN'; do
  expect_failure "chart client error '$error'" env CLIENT_ERROR="$error" \
    "$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
    absent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz"
done
# Captured against Quay with Helm v3.18.4 (gd80839c). The Linux/amd64 archive
# used by the release runner was checksum-verified as
# f8180838c23d7c7d797b208861fecb591d9ce1690d8704ed1e4cb8e2add966c1.
# The same output shape was confirmed on Linux/arm64 and Darwin/arm64.
helm_absence='Error: failed to perform "FetchReference" on source: quay.io/stackdome/charts/absent:1.0.0: not found'
CLIENT_ERROR="$helm_absence" \
  "$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
  absent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz"
for error in \
  "$helm_absence; unexpected EOF" \
  "$helm_absence; context deadline exceeded" \
  "$helm_absence; read: connection reset by peer" \
  "$helm_absence; lookup quay.io: no such host" \
  "$helm_absence; unexpected end of JSON input" \
  "$helm_absence"$'\nMANIFEST_UNKNOWN: partial response' \
  "$helm_absence"$'\n'"$helm_absence" \
  $'transport failed\n'"$helm_absence"; do
  expect_failure "mixed chart absence response '$error'" env CLIENT_ERROR="$error" \
    "$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
    absent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz"
done
for error in \
  'Error: failed to perform "FetchReference" on source: quay.io/stackdome/other/absent:1.0.0: not found' \
  'Error: failed to perform "FetchReference" on source: quay.io/stackdome/charts/other:1.0.0: not found' \
  'Error: failed to perform "FetchReference" on source: quay.io/stackdome/charts/absent-extra:1.0.0: not found' \
  'Error: failed to perform "FetchReference" on source: quay.io/stackdome/charts/absent:1.0.1: not found' \
  'Error: failed to perform "FetchReference" on source: QUAY.io/stackdome/charts/absent:1.0.0: not found' \
  'Error: failed to perform "FetchReference" on source: quay.io/stackdome/charts/absent:1.0.%30: not found' \
  'Error: failed to perform "FetchReference" on source: GET "https://quay.io/v2/stackdome/charts/absent/manifests/1.0.0": response status code 404: manifest unknown: manifest unknown'; do
  expect_failure "chart absence for another coordinate '$error'" env CLIENT_ERROR="$error" \
    "$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
    absent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz"
done
EXPECTED_HELM_VERSION='1.0.0+build.1' \
  CLIENT_ERROR='Error: failed to perform "FetchReference" on source: quay.io/stackdome/charts/absent:1.0.0_build.1: not found' \
  "$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
  absent 1.0.0+build.1 "$tmp/stackdome-agent-1.0.0.tgz"
expect_failure "Helm absence without build metadata normalization" env \
  EXPECTED_HELM_VERSION='1.0.0+build.1' \
  CLIENT_ERROR='Error: failed to perform "FetchReference" on source: quay.io/stackdome/charts/absent:1.0.0+build.1: not found' \
  "$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
  absent 1.0.0+build.1 "$tmp/stackdome-agent-1.0.0.tgz"
"$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts stackdome-agent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz"
chart_digest="$("$publisher" publish-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts stackdome-agent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz")"
test "$chart_digest" = "$("$publisher" publish-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts stackdome-agent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz")"
for mode in missing multiple malformed; do
  expect_failure "$mode chart pull digest" env HELM_PULL_DIGEST_MODE="$mode" \
    "$publisher" publish-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
    stackdome-agent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz"
done

for mode in missing multiple malformed; do
  archive="$tmp/stackdome-new_$mode-1.0.0.tgz"
  cp "$tmp/stackdome-agent-1.0.0.tgz" "$archive"
  expect_failure "$mode chart push digest" env HELM_PUSH_DIGEST_MODE="$mode" \
    "$publisher" publish-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
    "stackdome-new_$mode" 1.0.0 "$archive"
done

contradictory="sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
cp "$tmp/stackdome-agent-1.0.0.tgz" "$tmp/stackdome-contradictory-1.0.0.tgz"
expect_failure "contradictory push and pull chart digests" env HELM_STORED_DIGEST="$contradictory" \
  "$publisher" publish-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts \
  stackdome-contradictory 1.0.0 "$tmp/stackdome-contradictory-1.0.0.tgz"
printf 'different bytes' >"$tmp/different.tgz"
if "$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts stackdome-agent 1.0.0 "$tmp/different.tgz" >/dev/null 2>&1; then
  echo "mismatched existing chart coordinate unexpectedly passed" >&2
  exit 1
fi

echo "release publication coordinate tests passed"
