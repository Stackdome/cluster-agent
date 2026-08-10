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
   `./hack/test-release-publication.sh`, `./hack/test-release-recovery.rb`, and
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
It generates SPDX and CycloneDX SBOMs and rejects any fixable CRITICAL
vulnerability. Only then does it assign the final image tags and publish the
standalone and umbrella charts.

Trivy is downloaded and checksum-verified before Quay login. The release does
not execute Trivy composite actions or source installer code from the archive.
Each SBOM and scan report is validated against its exact image index digest and
linux architecture before publication.

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
evidence and `publication-state.txt` contains all two image tags, two chart
pushes, and four attestations. A failed run leaves a partial version that must
not be deployed.

Re-run the same reviewed tag only after inspecting the partial state. The
workflow preflights all four final OCI coordinates before writing any of them.
An absent coordinate or an existing coordinate with the exact expected digest
is retryable. A different image digest or different deterministic chart archive
fails closed; do not overwrite or delete it. Quarantine the partial coordinates
from deployment and prepare a new patch/prerelease version. Attestation retries
may add another attestation for the same immutable subject; they never change
the subject digest.

## CRD rollback drill

The workflow compares the candidate CRDs with the rollback chart before any
release publication and records that prior chart's CRDs as
`rollback-crds.yaml`. Helm does not roll back files in a chart's `crds/`
directory. Run this drill against the exact alpha cluster before approving the
release:

```bash
# Back up tenant custom resources and capture the installed controller revision.
kubectl get clusterregistries,stacks,stackresources,imagebuilds -A -o yaml >pre-rollback-custom-resources.yaml
kubectl scale deployment/<agent-deployment> --replicas=0 -n <namespace>

# Restore and verify the CRD specs captured from the recorded rollback chart.
kubectl apply --server-side --field-manager=stackdome-crd-rollback -f rollback-crds.yaml
crd_names="$(ruby -ryaml -e 'puts YAML.load_stream(File.read(ARGV[0])).compact.map { |crd| crd.dig("metadata", "name") }.join(" ")' rollback-crds.yaml)"
kubectl get customresourcedefinitions $crd_names -o yaml >installed-rollback-crds.yaml
./hack/verify-installed-crds.rb rollback-crds.yaml installed-rollback-crds.yaml

# Roll the controller back only after the prior CRD contract is installed.
helm rollback <release-name> <previous-revision> --namespace <namespace> --wait
kubectl rollout status deployment/<agent-deployment> -n <namespace> --timeout=5m
kubectl wait --for=condition=Available deployment/<agent-deployment> -n <namespace> --timeout=5m
```

Finally create, read, update, and delete one disposable resource for every CRD
reconciled by the previous controller and confirm existing tenant resources
still reconcile. Attach the commands, controller logs, CRD verification output,
and backup checksum to the launch log. If the prior bundle cannot be applied or
the smoke test fails, keep the controller scaled to zero, restore the backed-up
custom resources and cluster control-plane backup, and do not deploy that
release.
