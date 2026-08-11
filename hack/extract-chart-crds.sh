#!/usr/bin/env bash
set -euo pipefail

archive="${1:?usage: extract-chart-crds.sh <umbrella-chart.tgz> <destination>}"
destination="${2:?}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$destination"
tar -xzf "$archive" -C "$tmp"

crd_dir="$(find "$tmp" -type d -path '*/charts/stackdome-agent-standalone/crds' -print -quit)"
if [[ -z "$crd_dir" ]]; then
  nested="$(find "$tmp" -type f -name 'stackdome-agent-standalone-*.tgz' -print -quit)"
  test -n "$nested"
  mkdir -p "$tmp/nested"
  tar -xzf "$nested" -C "$tmp/nested"
  crd_dir="$(find "$tmp/nested" -type d -path '*/crds' -print -quit)"
fi
test -n "$crd_dir"
cp "$crd_dir"/*.{yaml,yml} "$destination"/ 2>/dev/null || cp "$crd_dir"/*.yaml "$destination"/
test "$(find "$destination" -type f -name '*.yaml' | wc -l | tr -d ' ')" -gt 0
