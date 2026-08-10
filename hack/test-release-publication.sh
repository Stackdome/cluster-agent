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
    if [[ -n "${CLIENT_ERROR:-}" ]]; then printf '%s\n' "$CLIENT_ERROR" >&2; exit 1; fi
    key="$(printf '%s' "$2" | tr '/:' '__')"
    if [[ ! -f "$STATE/$key" ]]; then echo MANIFEST_UNKNOWN >&2; exit 1; fi
    if [[ -n "${CRANE_DIGEST_OUTPUT:-}" ]]; then printf '%s\n' "$CRANE_DIGEST_OUTPUT"; exit 0; fi
    cat "$STATE/$key"
    ;;
  tag)
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
    ref="$2"; version="$4"; destination="$6"; name="${ref##*/}"
    key="$(printf '%s' "$name:$version" | tr '/:' '__')"
    if [[ -n "${CLIENT_ERROR:-}" ]]; then printf '%s\n' "$CLIENT_ERROR" >&2; exit 1; fi
    if [[ ! -f "$STATE/$key.tgz" ]]; then echo 'MANIFEST_UNKNOWN: manifest unknown' >&2; exit 1; fi
    cp "$STATE/$key.tgz" "$destination/$name-$version.tgz"
    case "${HELM_PULL_DIGEST_MODE:-valid}" in
      valid) printf 'Digest: %s\n' "$(cat "$STATE/$key.digest")" ;;
      missing) printf 'Pulled: %s\n' "$ref" ;;
      multiple) printf 'Digest: %s\nDigest: %s\n' "$(cat "$STATE/$key.digest")" "$(cat "$STATE/$key.digest")" ;;
      malformed) printf 'Digest: sha256:not-a-digest\n' ;;
    esac
    ;;
  push)
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
