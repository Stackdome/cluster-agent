#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/previous" "$tmp/current"

write_crd() {
  local path="$1" field_type="$2" required="$3"
  cat >"$path" <<YAML
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: examples.stackdome.io
spec:
  group: stackdome.io
  names:
    kind: Example
    plural: examples
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              required: $required
              properties:
                value:
                  type: $field_type
YAML
}

write_crd "$tmp/previous/example.yaml" string '[]'
write_crd "$tmp/current/example.yaml" string '[]'
"$repo_root/hack/verify-crd-compatibility.rb" --previous "$tmp/previous" --current "$tmp/current"

cp "$tmp/current/example.yaml" "$tmp/current/extra.yaml"
sed -i.bak 's/examples.stackdome.io/extra.stackdome.io/' "$tmp/current/extra.yaml"
if "$repo_root/hack/verify-crd-compatibility.rb" --previous "$tmp/previous" --current "$tmp/current" >/dev/null 2>&1; then
  echo "new rollback-unsafe CRD unexpectedly passed" >&2
  exit 1
fi
rm "$tmp/current/extra.yaml" "$tmp/current/extra.yaml.bak"

write_crd "$tmp/current/example.yaml" integer '[]'
if "$repo_root/hack/verify-crd-compatibility.rb" --previous "$tmp/previous" --current "$tmp/current" >/dev/null 2>&1; then
  echo "CRD type mutation unexpectedly passed" >&2
  exit 1
fi

write_crd "$tmp/current/example.yaml" string '[value]'
if "$repo_root/hack/verify-crd-compatibility.rb" --previous "$tmp/previous" --current "$tmp/current" >/dev/null 2>&1; then
  echo "new required CRD field unexpectedly passed" >&2
  exit 1
fi

cp "$tmp/previous/example.yaml" "$tmp/rollback-crds.yaml"
ruby -ryaml - "$tmp/previous/example.yaml" "$tmp/installed.yaml" <<'RUBY'
source, destination = ARGV
crd = YAML.safe_load(File.read(source))
crd["spec"]["preserveUnknownFields"] = false
File.write(destination, YAML.dump({"apiVersion" => "v1", "kind" => "List", "items" => [crd]}))
RUBY
"$repo_root/hack/verify-installed-crds.rb" "$tmp/rollback-crds.yaml" "$tmp/installed.yaml"

ruby -ryaml - "$tmp/installed.yaml" <<'RUBY'
path = ARGV.fetch(0)
document = YAML.safe_load(File.read(path))
document.dig("items", 0, "spec", "versions", 0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "value")["type"] = "integer"
File.write(path, YAML.dump(document))
RUBY
if "$repo_root/hack/verify-installed-crds.rb" "$tmp/rollback-crds.yaml" "$tmp/installed.yaml" >/dev/null 2>&1; then
  echo "installed CRD mismatch unexpectedly passed" >&2
  exit 1
fi

echo "CRD compatibility negative tests passed"
