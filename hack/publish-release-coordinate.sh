#!/usr/bin/env bash
set -euo pipefail

mode="${1:?usage: publish-release-coordinate.sh <check-image|publish-image|check-chart|publish-chart> ...}"
shift

not_found() {
  grep -Eqi 'manifest unknown|manifest_unknown|name unknown|not found|404' "$1"
}

check_image() {
  local crane="$1" repository="$2" version="$3" expected_digest="$4"
  local error_file existing
  [[ "$expected_digest" =~ ^sha256:[0-9a-f]{64}$ ]]
  error_file="$(mktemp)"
  if existing="$("$crane" digest "$repository:$version" 2>"$error_file")"; then
    rm -f "$error_file"
    if [[ "$existing" != "$expected_digest" ]]; then
      echo "$repository:$version already resolves to $existing, expected $expected_digest; use a new version" >&2
      return 1
    fi
    printf '%s\n' "$existing"
    return 0
  fi
  if ! not_found "$error_file"; then
    cat "$error_file" >&2
    rm -f "$error_file"
    return 1
  fi
  rm -f "$error_file"
  return 2
}

check_chart() {
  local helm="$1" registry="$2" name="$3" version="$4" archive="$5"
  local tmp output pulled expected_sha actual_sha digest
  tmp="$(mktemp -d)"
  if output="$("$helm" pull "$registry/$name" --version "$version" --destination "$tmp" 2>&1)"; then
    pulled="$tmp/$name-$version.tgz"
    test -s "$pulled"
    expected_sha="$(sha256sum "$archive" | awk '{print $1}')"
    actual_sha="$(sha256sum "$pulled" | awk '{print $1}')"
    if [[ "$actual_sha" != "$expected_sha" ]]; then
      rm -rf "$tmp"
      echo "$registry/$name:$version already contains different chart bytes; use a new version" >&2
      return 1
    fi
    digest="$(sed -n 's/^Digest: \(sha256:[0-9a-f]\{64\}\)$/\1/p' <<<"$output")"
    rm -rf "$tmp"
    [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]
    printf '%s\n' "$digest"
    return 0
  fi
  rm -rf "$tmp"
  if ! grep -Eqi 'manifest unknown|manifest_unknown|name unknown|not found|404' <<<"$output"; then
    printf '%s\n' "$output" >&2
    return 1
  fi
  return 2
}

case "$mode" in
  check-image)
    check_image "$@" || status=$?
    [[ "${status:-0}" == 0 || "${status:-0}" == 2 ]]
    ;;
  publish-image)
    crane="$1"; repository="$2"; version="$3"; expected_digest="$4"
    if digest="$(check_image "$@")"; then
      printf '%s\n' "$digest"
      exit 0
    elif [[ $? != 2 ]]; then
      exit 1
    fi
    "$crane" tag "$repository@$expected_digest" "$version"
    check_image "$crane" "$repository" "$version" "$expected_digest"
    ;;
  check-chart)
    check_chart "$@" || status=$?
    [[ "${status:-0}" == 0 || "${status:-0}" == 2 ]]
    ;;
  publish-chart)
    helm="$1"; registry="$2"; name="$3"; version="$4"; archive="$5"
    if digest="$(check_chart "$@")"; then
      printf '%s\n' "$digest"
      exit 0
    elif [[ $? != 2 ]]; then
      exit 1
    fi
    output="$("$helm" push "$archive" "$registry" 2>&1)"
    printf '%s\n' "$output" >&2
    digest="$(sed -n 's/^Digest: \(sha256:[0-9a-f]\{64\}\)$/\1/p' <<<"$output")"
    [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]
    printf '%s\n' "$digest"
    ;;
  *)
    echo "unknown publication mode: $mode" >&2
    exit 2
    ;;
esac
