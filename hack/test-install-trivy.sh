#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

printf '#!/usr/bin/env sh\necho test-trivy\n' >"$tmp/trivy"
chmod +x "$tmp/trivy"
tar -czf "$tmp/trivy.tar.gz" -C "$tmp" trivy
checksum="$(sha256sum "$tmp/trivy.tar.gz" | awk '{print $1}')"

STACKDOME_RELEASE_TOOL_TESTING=1 \
TRIVY_TEST_ARCHIVE="$tmp/trivy.tar.gz" \
TRIVY_TEST_SHA256="$checksum" \
  "$repo_root/hack/install-trivy.sh" "$tmp/trivy-bin"
test "$("$tmp/trivy-bin")" = "test-trivy"

printf 'corruption' >>"$tmp/trivy.tar.gz"
if STACKDOME_RELEASE_TOOL_TESTING=1 \
  TRIVY_TEST_ARCHIVE="$tmp/trivy.tar.gz" \
  TRIVY_TEST_SHA256="$checksum" \
  "$repo_root/hack/install-trivy.sh" "$tmp/should-not-exist" >/dev/null 2>&1; then
  echo "corrupted Trivy archive unexpectedly passed checksum validation" >&2
  exit 1
fi
test ! -e "$tmp/should-not-exist"

echo "Trivy installer checksum test passed"
