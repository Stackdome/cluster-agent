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
7. Run `make test`, `./hack/verify-release-assets.sh`, and
   `./hack/test-release-provenance.sh`.

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
  --set-string stackdome-agent-standalone.image.digest=sha256:<digest>
```

Rollback uses the last known Helm revision:

```bash
helm rollback <release-name> <previous-revision> --namespace <namespace>
```
