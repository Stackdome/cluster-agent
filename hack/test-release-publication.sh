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
    key="$(printf '%s' "$2" | tr '/:' '__')"
    if [[ ! -f "$STATE/$key" ]]; then echo MANIFEST_UNKNOWN >&2; exit 1; fi
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
    if [[ ! -f "$STATE/$key.tgz" ]]; then echo 'manifest unknown' >&2; exit 1; fi
    cp "$STATE/$key.tgz" "$destination/$name-$version.tgz"
    printf 'Digest: %s\n' "$(cat "$STATE/$key.digest")"
    ;;
  push)
    archive="$2"; registry="$3"; base="$(basename "$archive" .tgz)"
    name="${base%-*}"; version="${base##*-}"
    key="$(printf '%s' "$name:$version" | tr '/:' '__')"
    cp "$archive" "$STATE/$key.tgz"
    digest="sha256:$(sha256sum "$archive" | awk '{print $1}')"
    printf '%s\n' "$digest" >"$STATE/$key.digest"
    printf 'Digest: %s\n' "$digest"
    ;;
esac
SH
chmod +x "$tmp/bin/crane" "$tmp/bin/helm"

export STATE="$tmp/state"
publisher="$repo_root/hack/publish-release-coordinate.sh"
image_digest="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

"$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/agent v1 "$image_digest"
test "$("$publisher" publish-image "$tmp/bin/crane" quay.io/stackdome/agent v1 "$image_digest")" = "$image_digest"
test "$("$publisher" publish-image "$tmp/bin/crane" quay.io/stackdome/agent v1 "$image_digest")" = "$image_digest"
if "$publisher" check-image "$tmp/bin/crane" quay.io/stackdome/agent v1 \
  sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb >/dev/null 2>&1; then
  echo "mismatched existing image coordinate unexpectedly passed" >&2
  exit 1
fi

printf 'deterministic chart bytes' >"$tmp/stackdome-agent-1.0.0.tgz"
"$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts stackdome-agent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz"
chart_digest="$("$publisher" publish-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts stackdome-agent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz")"
test "$chart_digest" = "$("$publisher" publish-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts stackdome-agent 1.0.0 "$tmp/stackdome-agent-1.0.0.tgz")"
printf 'different bytes' >"$tmp/different.tgz"
if "$publisher" check-chart "$tmp/bin/helm" oci://quay.io/stackdome/charts stackdome-agent 1.0.0 "$tmp/different.tgz" >/dev/null 2>&1; then
  echo "mismatched existing chart coordinate unexpectedly passed" >&2
  exit 1
fi

echo "release publication coordinate tests passed"
