#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

expected_tag="${1:-}"

ruby -ryaml - "$expected_tag" <<'RUBY'
expected_tag = ARGV.fetch(0)

def fail_check(message)
  warn "release asset verification failed: #{message}"
  exit 1
end

standalone = YAML.safe_load(File.read("charts/stackdome-agent-standalone/Chart.yaml"))
umbrella = YAML.safe_load(File.read("charts/stackdome-agent/Chart.yaml"))

version = standalone.fetch("version").to_s
app_version = standalone.fetch("appVersion").to_s
fail_check("standalone appVersion must be v<version>") unless app_version == "v#{version}"
fail_check("umbrella chart version differs from standalone") unless umbrella.fetch("version").to_s == version
fail_check("umbrella appVersion differs from standalone") unless umbrella.fetch("appVersion").to_s == app_version
fail_check("tag #{expected_tag} differs from chart #{app_version}") unless expected_tag.empty? || expected_tag == app_version

dependencies = umbrella.fetch("dependencies")
expected = {
  "cert-manager" => "1.17.4",
  "cloudnative-pg" => "0.23.2",
  "plugin-barman-cloud" => "0.6.0",
  "traefik" => "34.0.0",
  "stackdome-agent-standalone" => version,
}
actual = dependencies.to_h { |dependency| [dependency.fetch("name"), dependency.fetch("version").to_s] }
fail_check("umbrella dependencies are not exact release inputs") unless actual == expected

lock = YAML.safe_load(File.read("charts/stackdome-agent/Chart.lock"))
locked = lock.fetch("dependencies").to_h do |dependency|
  [dependency.fetch("name"), dependency.fetch("version").to_s.sub(/^v(?=\d)/, "")]
end
fail_check("Chart.lock differs from Chart.yaml") unless locked == expected
RUBY

diff -qr config/deploy/crds charts/stackdome-agent-standalone/crds

rendered="$(mktemp)"
package_dir="$(mktemp -d)"
trap 'rm -f "$rendered"; rm -rf "$package_dir"' EXIT
helm template release-check charts/stackdome-agent-standalone \
  --namespace stackdome-control-plane >"$rendered"

ruby -ryaml -rjson - "$rendered" <<'RUBY'
rendered_path = ARGV.fetch(0)

def cluster_role(path)
  YAML.load_stream(File.read(path)).compact.find { |item| item["kind"] == "ClusterRole" }
end

def normalized_rules(role)
  role.fetch("rules").map do |rule|
    rule.transform_values { |value| value.is_a?(Array) ? value.map(&:to_s).sort : value }
  end.sort_by { |rule| JSON.generate(rule) }
end

deploy = cluster_role("config/deploy/01-rbac.yaml")
chart = cluster_role(rendered_path)
abort "release RBAC differs from config/deploy/01-rbac.yaml" unless normalized_rules(deploy) == normalized_rules(chart)

requirements = [
  ["registry.stackdome.io", "clusterregistries/finalizers", "update"],
  ["apps", "statefulsets", "delete"],
  ["", "pods", "delete"],
  ["", "persistentvolumeclaims", "delete"],
]
requirements.each do |api_group, resource, verb|
  found = deploy.fetch("rules").any? do |rule|
    rule.fetch("apiGroups", []).include?(api_group) &&
      rule.fetch("resources", []).include?(resource) &&
      rule.fetch("verbs", []).include?(verb)
  end
  abort "release RBAC lacks #{api_group}:#{resource}:#{verb}" unless found
end
RUBY

helm dependency build charts/stackdome-agent >/dev/null
while read -r checksum archive; do
  printf '%s  %s\n' "$checksum" "charts/stackdome-agent/charts/$archive"
done <hack/release-dependency-checksums.txt | shasum -a 256 --check

version="$(ruby -ryaml -e 'puts YAML.safe_load(File.read(ARGV.fetch(0))).fetch("version")' charts/stackdome-agent-standalone/Chart.yaml)"
app_version="$(ruby -ryaml -e 'puts YAML.safe_load(File.read(ARGV.fetch(0))).fetch("appVersion")' charts/stackdome-agent-standalone/Chart.yaml)"
repository="$(ruby -ryaml -e 'puts YAML.safe_load(File.read(ARGV.fetch(0))).dig("image", "repository")' charts/stackdome-agent-standalone/values.yaml)"
reconciler_image="quay.io/stackdome/registry-config-reconciler:${app_version}"
grep -Fq "RegistryConfigReconcilerImage = \"$reconciler_image\"" pkg/config/images.go
standalone_archive="charts/stackdome-agent/charts/stackdome-agent-standalone-${version}.tgz"
test -s "$standalone_archive"
tar -xOf "$standalone_archive" stackdome-agent-standalone/Chart.yaml \
  | ruby -ryaml -e 'source = YAML.safe_load(File.read(ARGV.fetch(0))); packaged = YAML.safe_load(STDIN.read); abort "packaged standalone metadata differs from source" unless packaged == source' \
      charts/stackdome-agent-standalone/Chart.yaml
tar -xzf "$standalone_archive" -C "$package_dir"
cp charts/stackdome-agent-standalone/Chart.yaml "$package_dir/stackdome-agent-standalone/Chart.yaml"
diff -qr charts/stackdome-agent-standalone "$package_dir/stackdome-agent-standalone"

default_image="$(helm template release-check charts/stackdome-agent-standalone \
  --namespace stackdome-control-plane | ruby -ryaml -e 'puts YAML.load_stream(STDIN.read).compact.find { |item| item["kind"] == "Deployment" }.dig("spec", "template", "spec", "containers", 0, "image")')"
expected_image="${repository}:${app_version}"
test "$default_image" = "$expected_image"

digest='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
digest_image="$(helm template release-check charts/stackdome-agent-standalone \
  --namespace stackdome-control-plane --set-string "image.digest=$digest" \
  | ruby -ryaml -e 'puts YAML.load_stream(STDIN.read).compact.find { |item| item["kind"] == "Deployment" }.dig("spec", "template", "spec", "containers", 0, "image")')"
test "$digest_image" = "${repository}@$digest"

cloud_args="$(helm template release-check charts/stackdome-agent-standalone \
  --namespace stackdome-control-plane \
  --set-string "registryConfigReconciler.digest=$digest" \
  --set "registryConfigReconciler.requireDigest=true" \
  | ruby -ryaml -e 'deployment = YAML.load_stream(STDIN.read).compact.find { |item| item["kind"] == "Deployment" }; puts deployment.dig("spec", "template", "spec", "containers", 0, "args")')"
grep -Fq -- "--registry-config-reconciler-image=${reconciler_image%:*}@$digest" <<<"$cloud_args"
grep -Fq -- "--require-registry-config-reconciler-digest=true" <<<"$cloud_args"

if helm template release-check charts/stackdome-agent-standalone \
  --namespace stackdome-control-plane \
  --set "registryConfigReconciler.requireDigest=true" >/dev/null 2>&1; then
  echo "cloud render unexpectedly accepted a tagged reconciler image" >&2
  exit 1
fi

echo "release assets verified for $app_version"
