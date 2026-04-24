# Cluster Agent

Kubernetes operator built with KubeBuilder v4 that manages multiple CRDs for the Stackdome platform.

## Project Structure

- `api/` — CRD type definitions organized by API group (`core/v1alpha1`, `addons/v1alpha1`, `builds/v1alpha1`, `registry/v1alpha1`, `storage/v1alpha1`, `users/v1alpha1`)
- `internal/controller/` — Reconciler implementations for each CRD
- `cmd/cluster-agent/` — Main operator entrypoint
- `pkg/` — Shared packages (interpolation, validators, etc.)
- `config/deploy/` — Kubernetes manifests (CRDs, RBAC, namespace)
- `test/integration/` — Integration test suite

## Integration Test Suite

### Running

```bash
make test-integration
```

Results are logged to `test/integration/last-run.log` (gitignored). Always check this file to review failures.

The test requires Docker (for Kind) and takes ~11 minutes end-to-end.

### Environment Variables

| Variable | Effect |
|---|---|
| `KEEP_CLUSTER=true` | Preserves the Kind cluster after tests finish (useful for debugging) |
| `OPERATOR_IMAGE=<image>` | Skips building the operator and uses the provided image instead |

### Architecture

The suite uses Ginkgo v2 + Gomega with a shared Kind cluster. The cluster is created once in `BeforeSuite` and torn down in `DeferCleanup`. All test files share the same `bootstrap.Environment`.

```
test/integration/
  integration_suite_test.go    # Ginkgo entry, BeforeSuite bootstrap, shared Environment
  postgres_addon_test.go       # PostgresCluster addon lifecycle tests
  stack_lifecycle_test.go      # Stack/StackResource lifecycle tests
  bootstrap/
    bootstrap.go               # Setup orchestrator (3 phases)
    cluster.go                 # Kind cluster lifecycle via devkube
    operator.go                # Build, containerize, and deploy operator into Kind
    prerequisites.go           # Test namespace, ImageCatalog
    s3mock.go                  # S3Mock deployment + ObjectStore CR
  fixtures/
    fixtures.go                # PostgresCluster factory functions
    stack_fixtures.go          # Stack factory functions
  helpers/
    helpers.go                 # PostgresCluster polling/wait utilities
    stack_helpers.go           # Stack/StackResource polling/wait utilities
```

### Bootstrap Flow

The bootstrap runs 3 phases before any test spec executes:

**Phase 1 — Cluster + Infrastructure**
1. Deletes any stale Kind cluster named `stackdome-int-test` (avoids devkube reuse of invalid kubeconfigs)
2. Creates a 3-node Kind cluster (1 control plane + 2 workers) via `devkube`
3. Installs CRDs from `config/deploy/crds/`
4. Installs CNPG operator (Helm chart from `cloudnative-pg.github.io/charts`)
5. Installs Cert-Manager (Helm chart from `charts.jetstack.io`)
6. Applies Barman Cloud plugin manifest
7. Deploys S3Mock (namespace, Deployment, Service) — cluster-level infra for backup tests

**Phase 2 — Operator Deployment**
1. Builds the operator binary for `linux/amd64` (`go build`)
2. Creates a minimal Docker image with the binary
3. Loads the image into Kind (`kind load docker-image`)
4. Applies namespace + RBAC from `config/deploy/00-namespace.yaml` and `config/deploy/01-rbac.yaml`
5. Creates the operator Deployment in `stackdome-control-plane` namespace

**Phase 3 — Test Prerequisites**
1. Creates test namespace `pg-integration-test`
2. Creates CNPG ImageCatalog with PostgreSQL 16
3. Creates S3Mock credentials Secret and Barman ObjectStore CR in the test namespace

### Test Pattern

Each test context follows the same pattern:
- Tests within a context are `Ordered` — they run sequentially and share state
- `BeforeAll` gets the shared `Environment` (client, namespace, etc.)
- Individual `It` specs create CRs, wait for readiness, and verify behavior
- `AfterAll` cleans up CRs created by that context
- Each test is responsible for cleaning up its own artifacts

### Key CRDs Tested

**Stack** (`core.stackdome.io/v1alpha1`): Contains `StackResources[]` templates. Stack controller creates child StackResource objects. Stack becomes Ready when all StackResources report `Available=True`.

**StackResource** (`core.stackdome.io/v1alpha1`): Represents a workload container. Uses `ImageSpec` (pre-built image) or `BuildSpec` (build from source). Creates Deployment + Service. Supports `dependsOn[]` for ordering, env var interpolation between siblings via `{{ STACKDOME_<NAME>_INTERNAL }}`.

**PostgresCluster** (`addons.stackdome.io/v1alpha1`): Manages CNPG Cluster, Databases, and ScheduledBackup CRs.

### Naming Conventions (from controller code)

- StackResource name = `StackResourceTemplate.Name` (no prefix from parent Stack)
- Deployment name = StackResource name
- Service name = StackResource name
- Pod label: `resource: <stackresource-name>`
- Ownership chain: Stack -> StackResource -> Deployment/Service (cascade delete)

## Debugging Integration Tests

### Step 1: Read the log file

Test output is saved to `test/integration/last-run.log`. Start here.

```bash
# See which specs failed
grep -A 5 "FAILED" test/integration/last-run.log

# See the full timeline
cat test/integration/last-run.log
```

### Step 2: Preserve the cluster for inspection

If the log isn't enough, re-run with `KEEP_CLUSTER=true` to keep the Kind cluster alive after the test:

```bash
KEEP_CLUSTER=true make test-integration
```

Then set the kubectl context:

```bash
kubectl cluster-info --context kind-stackdome-int-test
```

### Step 3: Inspect operator logs

The operator runs in the `stackdome-control-plane` namespace:

```bash
# Get operator pod logs
kubectl logs -n stackdome-control-plane deployment/stackdome-operator-manager --tail=200

# Follow logs in real time
kubectl logs -n stackdome-control-plane deployment/stackdome-operator-manager -f

# Check if the operator pod is healthy
kubectl get pods -n stackdome-control-plane
```

### Step 4: Inspect CRs and child resources

Check the state of the CR that the test is working with:

```bash
# Stacks and StackResources
kubectl get stacks -n pg-integration-test
kubectl get stackresources -n pg-integration-test
kubectl describe stack <name> -n pg-integration-test
kubectl describe stackresource <name> -n pg-integration-test

# PostgresClusters and CNPG resources
kubectl get postgresclusters -n pg-integration-test
kubectl get clusters.postgresql.cnpg.io -n pg-integration-test
kubectl get databases.postgresql.cnpg.io -n pg-integration-test
kubectl get scheduledbackups.postgresql.cnpg.io -n pg-integration-test

# Deployments, Services created by StackResource controller
kubectl get deployments -n pg-integration-test
kubectl get services -n pg-integration-test

# Events can reveal why a resource is stuck
kubectl get events -n pg-integration-test --sort-by='.lastTimestamp'
```

### Step 5: Check cluster dependencies

If bootstrap itself fails, check the infrastructure components:

```bash
# CNPG operator
kubectl get pods -n cnpg-system

# Cert-Manager
kubectl get pods -n cert-manager

# S3Mock (for backup tests)
kubectl get pods -n s3mock

# CRDs installed
kubectl get crds | grep stackdome
kubectl get crds | grep cnpg
```

### Common Failure Patterns

**Bootstrap fails with "no configuration has been provided"**: A stale Kind cluster with the same name exists from a previous run. The `deleteExistingCluster` in `cluster.go` should handle this, but if the cache at `.cache/integration-test/` is corrupted, delete it manually: `rm -rf .cache/integration-test/stackdome-int-test`.

**Test times out waiting for Stack/PostgresCluster Ready**: Check operator logs for reconciliation errors. Common causes: image pull failures in Kind (image not loaded), RBAC missing permissions, CRD not installed.

**StackResource stuck in Pending with "DependenciesNotReady"**: The `dependsOn` target StackResource hasn't reached `Available=True` yet. Check if the dependency's Deployment pods are running and the Service was created.

**PostgresCluster stuck**: CNPG Cluster takes 1-3 minutes to become healthy. Check `kubectl get clusters.postgresql.cnpg.io -n pg-integration-test -o yaml` for status conditions and `kubectl get pods -n pg-integration-test` for PostgreSQL pod readiness.
