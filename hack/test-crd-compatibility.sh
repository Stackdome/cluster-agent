#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/previous" "$tmp/current"

cat >"$tmp/previous/example.yaml" <<'YAML'
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
              properties:
                value:
                  type: string
YAML

cp "$tmp/previous/example.yaml" "$tmp/current/example.yaml"
checker="$repo_root/hack/verify-crd-compatibility.rb"
installed_checker="$repo_root/hack/verify-installed-crds.rb"
prepare="$repo_root/hack/prepare-crd-rollback.rb"

verify_candidate() {
  "$checker" --previous "$tmp/previous" --current "$tmp/current"
}

expect_candidate_failure() {
  local description="$1" ruby_mutation="$2"
  cp "$tmp/previous/example.yaml" "$tmp/current/example.yaml"
  ruby -ryaml - "$tmp/current/example.yaml" "$ruby_mutation" <<'RUBY'
path, mutation = ARGV
document = YAML.safe_load(File.read(path))
eval(mutation)
File.write(path, YAML.dump(document))
RUBY
  if verify_candidate >/dev/null 2>&1; then
    echo "$description unexpectedly passed candidate CRD policy" >&2
    exit 1
  fi
}

verify_candidate

# These are the only documented API-server defaults ignored by the alpha policy.
ruby -ryaml - "$tmp/current/example.yaml" <<'RUBY'
path = ARGV.fetch(0)
document = YAML.safe_load(File.read(path))
document["spec"]["preserveUnknownFields"] = false
document["spec"]["conversion"] = {"strategy" => "None"}
document.dig("spec", "versions", 0)["deprecated"] = false
File.write(path, YAML.dump(document))
RUBY
verify_candidate

expect_candidate_failure "nullable mutation" \
  'document.dig("spec", "versions", 0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "value")["nullable"] = true'
expect_candidate_failure "additionalProperties mutation" \
  'document.dig("spec", "versions", 0, "schema", "openAPIV3Schema", "properties", "spec")["additionalProperties"] = false'
expect_candidate_failure "Kubernetes map semantics mutation" \
  'document.dig("spec", "versions", 0, "schema", "openAPIV3Schema", "properties", "spec")["x-kubernetes-map-type"] = "atomic"'
expect_candidate_failure "conversion mutation" \
  'document["spec"]["conversion"] = {"strategy" => "Webhook", "webhook" => {"conversionReviewVersions" => ["v1"], "clientConfig" => {"url" => "https://example.invalid"}}}'
expect_candidate_failure "storage topology mutation" \
  'document.dig("spec", "versions", 0)["storage"] = false; document["spec"]["versions"] << document.dig("spec", "versions", 0).merge("name" => "v1", "storage" => true)'
expect_candidate_failure "schema type mutation" \
  'document.dig("spec", "versions", 0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "value")["type"] = "integer"'

cp "$tmp/previous/example.yaml" "$tmp/current/extra.yaml"
sed -i.bak 's/examples.stackdome.io/extra.stackdome.io/' "$tmp/current/extra.yaml"
if verify_candidate >/dev/null 2>&1; then
  echo "new CRD unexpectedly passed candidate CRD policy" >&2
  exit 1
fi
rm "$tmp/current/extra.yaml" "$tmp/current/extra.yaml.bak"

cp "$tmp/previous/example.yaml" "$tmp/rollback-crds.yaml"
write_installed() {
  ruby -ryaml - "$tmp/previous/example.yaml" "$tmp/installed.yaml" <<'RUBY'
source, destination = ARGV
crd = YAML.safe_load(File.read(source))
crd["metadata"]["resourceVersion"] = "12345"
crd["spec"]["preserveUnknownFields"] = false
crd["spec"]["conversion"] = {"strategy" => "None"}
crd.dig("spec", "versions", 0)["deprecated"] = false
crd["status"] = {"storedVersions" => ["v1alpha1"]}
File.write(destination, YAML.dump({"apiVersion" => "v1", "kind" => "List", "items" => [crd]}))
RUBY
}

expect_installed_failure() {
  local description="$1" ruby_mutation="$2"
  write_installed
  ruby -ryaml - "$tmp/installed.yaml" "$ruby_mutation" <<'RUBY'
path, mutation = ARGV
document = YAML.safe_load(File.read(path))
eval(mutation)
File.write(path, YAML.dump(document))
RUBY
  if "$installed_checker" "$tmp/rollback-crds.yaml" "$tmp/installed.yaml" >/dev/null 2>&1; then
    echo "$description unexpectedly passed installed CRD verification" >&2
    exit 1
  fi
}

write_installed
"$installed_checker" "$tmp/rollback-crds.yaml" "$tmp/installed.yaml"
expect_installed_failure "extra installed conversion" \
  'document.dig("items", 0, "spec")["conversion"] = {"strategy" => "Webhook", "webhook" => {}}'
expect_installed_failure "extra installed schema field" \
  'document.dig("items", 0, "spec", "versions", 0, "schema", "openAPIV3Schema")["nullable"] = true'
expect_installed_failure "stored version mismatch" \
  'document.dig("items", 0, "status")["storedVersions"] = ["v1"]'
expect_installed_failure "served topology mismatch" \
  'document.dig("items", 0, "spec", "versions", 0)["served"] = false'

write_installed
"$prepare" "$tmp/rollback-crds.yaml" "$tmp/installed.yaml" >"$tmp/replacements.yaml"
ruby -ryaml - "$tmp/rollback-crds.yaml" "$tmp/replacements.yaml" <<'RUBY'
expected_path, replacement_path = ARGV
expected = YAML.load_stream(File.read(expected_path)).compact.fetch(0)
replacement = YAML.load_stream(File.read(replacement_path)).compact.fetch(0)
abort "rollback replacement did not preserve resourceVersion" unless replacement.dig("metadata", "resourceVersion") == "12345"
abort "rollback replacement did not use the complete prior spec" unless replacement["spec"] == expected["spec"]
abort "rollback replacement must not include status" if replacement.key?("status")
RUBY

echo "strict alpha CRD contract tests passed"
