# Cluster Agent Releases

The release workflow publishes one immutable release set from a reviewed tag.
It does not edit chart source during the release.

## Prepare a release

Use a pull request to update all release inputs before creating a tag:

1. Set both chart `version` fields to the same version.
2. Set both chart `appVersion` fields to `v<version>`.
3. Set the umbrella chart's standalone dependency to the same exact version.
4. Keep every external chart dependency at an exact version.
5. Run `helm dependency update charts/stackdome-agent`.
6. Update `hack/release-dependency-checksums.txt` from trusted upstream archives.
7. Run `make test`, `./hack/verify-release-assets.sh`,
   `./hack/test-release-provenance.sh`, `./hack/test-release-evidence.sh`,
   `./hack/test-release-publication.sh`, `./hack/test-release-recovery.sh`, and
   `./hack/test-crd-compatibility.sh`.

Merge the reviewed release inputs to the protected default branch. Create the
matching `v<version>` tag only after the merge.

## Required hosted controls

Configure these controls before creating a release tag:

- Protect the default branch. Require pull request review and the CI jobs.
- Add a GitHub tag ruleset for `refs/tags/v*`. Restrict tag creation. Block tag
  updates and deletion.
- Create a protected `agent-release` environment with an independent required
  reviewer.
- Store `QUAY_USER` and `QUAY_TOKEN` only in `agent-release`. Use a Quay robot
  that can write only these repositories:
  - `stackdome/cluster-agent/cluster-agent-manager`
  - `stackdome/registry-config-reconciler`
  - `stackdome/charts/stackdome-agent-standalone`
  - `stackdome/charts/stackdome-agent`
- Set the environment variable `ROLLBACK_CHART_VERSION` to the last approved
  chart version. The workflow proves that coordinate exists and records its
  OCI digest.
- Make final version tags immutable in all four Quay repositories.
- Enable GitHub artifact attestations for the repository.

Retain the branch rules, tag rules, environment approval, Quay robot scopes,
and immutable-tag settings as release evidence. A workflow file cannot prove
those hosted settings by itself.

## Published evidence

The workflow first pushes untagged linux/amd64 and linux/arm64 image content.
It preserves Syft's SPDX, CycloneDX, and native JSON output and rejects any
fixable CRITICAL vulnerability. Only then does it assign the final image tags
and publish the standalone and umbrella charts.

Trivy is downloaded and checksum-verified before Quay login. The release does
not execute Trivy composite actions or source installer code from the archive.
Each SPDX document's scanner-native OCI purl is validated against the exact
platform manifest in the retained raw image index. The raw CycloneDX root is
bound to that index through the Syft JSON produced by the same scanner
invocation, which records the selected manifest digest, OS, and architecture.
No subject identity is added to or changed in the SBOM files after scanning.

The release artifact records source commit, module and API versions, both image
digests, both chart digests, the CRD bundle checksum, SBOM and scan results, and
the rollback chart coordinate. The Go release packager fixes archive ownership
and timestamps to the tagged commit time. The umbrella package reuses the
dependency archives checked before publication; it does not fetch them again.
Install the agent image by digest with:

```bash
helm upgrade --install stackdome-agent \
  oci://quay.io/stackdome/charts/stackdome-agent \
  --version <version> \
  --set-string stackdome-agent-standalone.image.digest=sha256:<agent-digest> \
  --set-string stackdome-agent-standalone.registryConfigReconciler.digest=sha256:<reconciler-digest> \
  --set stackdome-agent-standalone.registryConfigReconciler.requireDigest=true
```

`registryConfigReconciler.requireDigest=true` is mandatory for Stackdome Cloud.
The manager exits before starting controllers if that flag is paired with a
tagged or malformed reconciler image reference.

## Interrupted publication

The release is complete only when `release-complete` exists in the uploaded
evidence. `publication-state.json` must contain the exact subject and digest for
both image tags, both chart manifests, and all four attestations. Every
attestation record also retains its action ID, validated bundle, and bundle
checksum. `release-state.rb complete` verifies that evidence against
`release-metadata.json` before it writes the completion marker and checksums. A
failed run leaves a partial version that must not be deployed.

Re-run the same reviewed tag only after inspecting the partial state. The
workflow preflights all four final OCI coordinates before writing any of them.
An absent coordinate or an existing coordinate with the exact expected digest
is retryable. A different image digest or different deterministic chart archive
fails closed; do not overwrite or delete it. Quarantine the partial coordinates
from deployment and prepare a new patch/prerelease version. Attestation retries
may add another attestation for the same immutable subject; they never change
the subject digest. The workflow verifies every registry referrer with
`gh attestation verify` and retains the exact bundle returned by the action.

## CRD rollback drill

The workflow compares the candidate CRDs with the rollback chart before any
release publication and records that prior chart's CRDs as
`rollback-crds.yaml`. Public-alpha releases prohibit every CRD spec change,
after canonicalizing only the documented API-server defaults recorded in
`hack/crd_contract.rb`. Helm does not roll back files in a chart's `crds/`
directory. The repository test is not a substitute for this live drill. Run it
against the exact alpha cluster before approving the release:

Before this drill, complete and verify controller-specific data-plane backups.
This includes a restorable PostgreSQL backup for every `PostgresCluster` and
provider snapshots or equivalent backups for registry, object-storage, NFS,
and persistent-volume data. Record the backup identifiers and restore-check
results in the launch log. Kubernetes custom-resource exports do not contain
that data and are not a substitute for these backups.

```bash
set -euo pipefail

# Derive every protected resource from the exact rollback CRD bundle. The
# fully qualified resource name avoids relying on kubectl short-name aliases.
ruby -ryaml -e '
  YAML.load_stream(File.read(ARGV.fetch(0))).compact.each do |crd|
    name = crd.dig("metadata", "name")
    group = crd.dig("spec", "group")
    plural = crd.dig("spec", "names", "plural")
    abort "rollback bundle contains an incomplete CRD" unless name && group && plural
    puts [name, "#{plural}.#{group}"].join("\t")
  end
' rollback-crds.yaml >protected-crd-resources.tsv
test -s protected-crd-resources.tsv

# Export every protected custom resource and capture the installed controller
# before any CRD mutation. Empty resource kinds still produce a valid List.
rm -rf pre-rollback-custom-resources
mkdir -p pre-rollback-custom-resources
while IFS=$'\t' read -r crd_name resource; do
  kubectl get "$resource" --all-namespaces -o yaml \
    >"pre-rollback-custom-resources/$crd_name.yaml"
done <protected-crd-resources.tsv
kubectl get deployment/<agent-deployment> -n <namespace> -o yaml \
  >pre-rollback-controller.yaml

# Verify every CRD from the bundle has one readable List export, then checksum
# the backup before changing the installed CRDs.
ruby -ryaml -e '
  rows = File.readlines(ARGV.fetch(0), chomp: true).reject(&:empty?)
  abort "rollback bundle contains no protected resources" if rows.empty?
  rows.each do |row|
    crd_name, resource = row.split("\t", 2)
    abort "invalid protected resource row: #{row}" unless crd_name && resource
    path = File.join(ARGV.fetch(1), "#{crd_name}.yaml")
    export = YAML.safe_load(File.read(path))
    unless export.is_a?(Hash) && export["kind"].to_s.end_with?("List") && export["items"].is_a?(Array)
      abort "#{path} is not a Kubernetes List export"
    end
  end
' protected-crd-resources.tsv pre-rollback-custom-resources
tar -czf pre-rollback-custom-resources.tar.gz pre-rollback-custom-resources
sha256sum pre-rollback-custom-resources.tar.gz pre-rollback-controller.yaml \
  >pre-rollback-backups.sha256
sha256sum --check pre-rollback-backups.sha256

kubectl scale deployment/<agent-deployment> --replicas=0 -n <namespace>

crd_names="$(ruby -ryaml -e 'puts YAML.load_stream(File.read(ARGV[0])).compact.map { |crd| crd.dig("metadata", "name") }.join(" ")' rollback-crds.yaml)"
kubectl get customresourcedefinitions $crd_names -o yaml >installed-before-rollback.yaml

# Fail before mutation if version topology or status.storedVersions cannot be
# restored safely. Each object retains its live resourceVersion and replaces
# the complete prior spec in one Kubernetes update.
./hack/prepare-crd-rollback.rb \
  rollback-crds.yaml installed-before-rollback.yaml >rollback-replacements.yaml
kubectl replace -f rollback-replacements.yaml

# Do not restart any controller until every prior spec and storage topology
# has been verified exactly.
kubectl get customresourcedefinitions $crd_names -o yaml >installed-rollback-crds.yaml
./hack/verify-installed-crds.rb rollback-crds.yaml installed-rollback-crds.yaml

# Roll the controller back only after the prior CRD contract is installed.
helm rollback <release-name> <previous-revision> --namespace <namespace> --wait
kubectl rollout status deployment/<agent-deployment> -n <namespace> --timeout=5m
kubectl wait --for=condition=Available deployment/<agent-deployment> -n <namespace> --timeout=5m
```

If `prepare-crd-rollback.rb` reports a version-topology or
`status.storedVersions` mismatch, keep the controller scaled to zero. Perform
and verify a Kubernetes storage-version migration using the cluster's supported
migration procedure, capture a fresh backup, and restart this drill. Never edit
`status.storedVersions` by hand.

Finally create, read, update, and delete one disposable resource for every CRD
reconciled by the previous controller and confirm existing tenant resources
still reconcile. Attach the commands, controller logs, CRD verification output,
custom-resource export checksum, and data-plane backup verification to the
launch log. If the prior bundle cannot be applied or the smoke test fails, keep
the controller scaled to zero, restore the custom resources, controller-specific
data-plane backups, and cluster control-plane backup, and do not deploy that
release.
