#!/usr/bin/env bash
set -euo pipefail

destination="${1:?usage: install-trivy.sh <destination>}"
version="0.73.0"
archive_name="trivy_${version}_Linux-64bit.tar.gz"
expected_sha256="2edd39da482bb4e9831962487b68f68e3928ec3137794757f54d00383d79547b"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
archive="$tmp/$archive_name"

if [[ "${STACKDOME_RELEASE_TOOL_TESTING:-}" == "1" ]]; then
  test -n "${TRIVY_TEST_ARCHIVE:-}"
  test -n "${TRIVY_TEST_SHA256:-}"
  cp "$TRIVY_TEST_ARCHIVE" "$archive"
  expected_sha256="$TRIVY_TEST_SHA256"
else
  if [[ -n "${STACKDOME_RELEASE_TOOL_TESTING:-}${TRIVY_TEST_ARCHIVE:-}${TRIVY_TEST_SHA256:-}" ]]; then
    echo "Trivy test overrides are forbidden outside explicit test mode" >&2
    exit 1
  fi
  curl --fail --location --silent --show-error \
    "https://github.com/aquasecurity/trivy/releases/download/v${version}/${archive_name}" \
    --output "$archive"
fi

printf '%s  %s\n' "$expected_sha256" "$archive" | sha256sum --check
tar -xzf "$archive" -C "$tmp" trivy
install -m 0755 "$tmp/trivy" "$destination"
