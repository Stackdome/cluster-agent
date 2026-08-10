#!/usr/bin/env bash
set -euo pipefail

mode="${1:?usage: publish-release-coordinate.sh <check-image|publish-image|check-chart|publish-chart> ...}"
shift

oci_manifest_url() {
  local coordinate="${1#oci://}" reference="$2"
  local registry repository

  [[ "$coordinate" == */* && "$coordinate" != *://* ]] || return 1
  registry="${coordinate%%/*}"
  repository="${coordinate#*/}"

  # These are the URL-path-safe character sets accepted by the pinned clients.
  # Rejecting anything else also rejects alternate percent-encoded spellings.
  [[ "$registry" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?$ ]] || return 1
  [[ "$repository" =~ ^[a-z0-9][a-z0-9._/-]*[a-z0-9]$ ]] || return 1
  [[ "$reference" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || return 1

  printf 'https://%s/v2/%s/manifests/%s\n' "$registry" "$repository" "$reference"
}

is_crane_manifest_absence() {
  local output="$1" expected_url="$2" terminal_url warning_url
  local terminal_pattern='^Error: GET (https://[^[:space:]]+): (MANIFEST_UNKNOWN: manifest unknown|NAME_UNKNOWN: repository name not known to registry)(; map\[\])?$'
  local warning_pattern='^[0-9]{4}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2} HEAD request failed, falling back on GET: HEAD (https://[^[:space:]]+): unexpected status code 404 Not Found \(HEAD responses have no body, use GET for details\)$'
  local line
  local -a lines
  lines=()
  while IFS= read -r line; do
    lines[${#lines[@]}]="$line"
  done <<<"$output"

  [[ "${#lines[@]}" == 1 || "${#lines[@]}" == 2 ]] || return 1
  [[ "${lines[$((${#lines[@]} - 1))]}" =~ $terminal_pattern ]] || return 1
  terminal_url="${BASH_REMATCH[1]}"
  [[ "$terminal_url" == "$expected_url" ]] || return 1
  if [[ "${#lines[@]}" == 2 ]]; then
    [[ "${lines[0]}" =~ $warning_pattern ]] || return 1
    warning_url="${BASH_REMATCH[1]}"
    [[ "$warning_url" == "$terminal_url" ]] || return 1
  fi
}

is_helm_manifest_absence() {
  local output="$1" expected_url="$2" response_url
  local absence_pattern='^Error: failed to perform "FetchReference" on source: GET "(https://[^"]+)": response status code 404: (manifest unknown: manifest unknown|name unknown: repository name not known to registry)$'
  [[ "$output" =~ $absence_pattern ]] || return 1
  response_url="${BASH_REMATCH[1]}"
  [[ "$response_url" == "$expected_url" ]]
}

require_digest() {
  local digest="$1" description="$2"
  if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "$description did not return exactly one SHA-256 digest" >&2
    return 1
  fi
}

parse_helm_digest() {
  local output="$1" digest_lines digest
  digest_lines="$(sed -n '/^Digest:/p' <<<"$output")"
  if [[ ! "$digest_lines" =~ ^Digest:\ (sha256:[0-9a-f]{64})$ ]]; then
    echo "Helm did not return exactly one valid Digest line" >&2
    return 1
  fi
  digest="${BASH_REMATCH[1]}"
  require_digest "$digest" "Helm"
  printf '%s\n' "$digest"
}

check_image() {
  local crane="$1" repository="$2" version="$3" expected_digest="$4"
  local error error_file existing expected_url
  require_digest "$expected_digest" "expected image"
  if ! expected_url="$(oci_manifest_url "$repository" "$version")"; then
    echo "$repository:$version is not a supported OCI image coordinate" >&2
    return 1
  fi
  error_file="$(mktemp)"
  if existing="$("$crane" digest "$repository:$version" 2>"$error_file")"; then
    rm -f "$error_file"
    require_digest "$existing" "$repository:$version"
    if [[ "$existing" != "$expected_digest" ]]; then
      echo "$repository:$version already resolves to $existing, expected $expected_digest; use a new version" >&2
      return 1
    fi
    printf '%s\n' "$existing"
    return 0
  fi

  error="$(cat "$error_file")"
  rm -f "$error_file"
  if ! is_crane_manifest_absence "$error" "$expected_url"; then
    printf '%s\n' "$error" >&2
    return 1
  fi
  return 2
}

check_chart() {
  local helm="$1" registry="$2" name="$3" version="$4" archive="$5"
  local expected_digest="${6:-}" tmp output pulled expected_sha actual_sha digest expected_url
  if [[ -n "$expected_digest" ]]; then
    require_digest "$expected_digest" "expected chart"
  fi
  if ! expected_url="$(oci_manifest_url "$registry/$name" "${version//+/_}")"; then
    echo "$registry/$name:$version is not a supported OCI chart coordinate" >&2
    return 1
  fi

  tmp="$(mktemp -d)"
  if output="$("$helm" pull "$registry/$name" --version "$version" --destination "$tmp" 2>&1)"; then
    pulled="$tmp/$name-$version.tgz"
    if [[ ! -s "$pulled" ]]; then
      rm -rf "$tmp"
      echo "Helm reported success without the expected chart archive" >&2
      return 1
    fi
    expected_sha="$(sha256sum "$archive" | awk '{print $1}')"
    actual_sha="$(sha256sum "$pulled" | awk '{print $1}')"
    if [[ "$actual_sha" != "$expected_sha" ]]; then
      rm -rf "$tmp"
      echo "$registry/$name:$version already contains different chart bytes; use a new version" >&2
      return 1
    fi
    if ! digest="$(parse_helm_digest "$output")"; then
      rm -rf "$tmp"
      return 1
    fi
    rm -rf "$tmp"
    if [[ -n "$expected_digest" && "$digest" != "$expected_digest" ]]; then
      echo "$registry/$name:$version resolved to $digest after push, expected $expected_digest" >&2
      return 1
    fi
    printf '%s\n' "$digest"
    return 0
  fi

  rm -rf "$tmp"
  if ! is_helm_manifest_absence "$output" "$expected_url"; then
    printf '%s\n' "$output" >&2
    return 1
  fi
  return 2
}

case "$mode" in
  check-image)
    if check_image "$@"; then
      exit 0
    else
      status=$?
    fi
    [[ "$status" == 2 ]] && exit 0
    exit "$status"
    ;;
  publish-image)
    crane="$1"; repository="$2"; version="$3"; expected_digest="$4"
    if digest="$(check_image "$@")"; then
      printf '%s\n' "$digest"
      exit 0
    else
      status=$?
    fi
    [[ "$status" == 2 ]] || exit "$status"
    "$crane" tag "$repository@$expected_digest" "$version"
    check_image "$crane" "$repository" "$version" "$expected_digest"
    ;;
  check-chart)
    if check_chart "$@"; then
      exit 0
    else
      status=$?
    fi
    [[ "$status" == 2 ]] && exit 0
    exit "$status"
    ;;
  publish-chart)
    helm="$1"; registry="$2"; name="$3"; version="$4"; archive="$5"
    if digest="$(check_chart "$@")"; then
      printf '%s\n' "$digest"
      exit 0
    else
      status=$?
    fi
    [[ "$status" == 2 ]] || exit "$status"
    output="$("$helm" push "$archive" "$registry" 2>&1)"
    printf '%s\n' "$output" >&2
    digest="$(parse_helm_digest "$output")"
    check_chart "$helm" "$registry" "$name" "$version" "$archive" "$digest"
    ;;
  *)
    echo "unknown publication mode: $mode" >&2
    exit 2
    ;;
esac
