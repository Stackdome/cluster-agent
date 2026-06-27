# Stackdome Cluster Agent

Kubernetes operator that extends clusters with a full developer platform stack. Built with [KubeBuilder v4](https://book.kubebuilder.io/) and controller-runtime, it manages workloads, storage, registries, databases, and user access through a set of declarative CRDs.

The cluster agent is deployed as a spoke-side component in the Stackdome platform architecture — each managed cluster runs its own agent to reconcile platform resources locally.

## What It Does

| Capability | CRDs | Description |
|---|---|---|
| **Workload Management** | `Stack`, `StackResource` | Deploy services, workers, jobs, and cron jobs with dependency ordering between resources |
| **Image Building** | `ImageBuild` | Build container images in-cluster using Kaniko from git repos or build contexts |
| **Storage** | `Volume`, `NFSServer`, `ObjectStorage` | Persistent volumes with git/remote sync, NFS-backed RWMany storage, and S3-compatible object storage (RustFS) |
| **Registry** | `ClusterRegistry` | In-cluster Docker registry (Zot) with auth, retention policies, and storage management |
| **Database** | `PostgresCluster` | HA PostgreSQL via CloudNativePG with backup/recovery (Barman), extensions (pgvector), and multi-database support |
| **Users** | `User` | Cluster-scoped user management with RBAC rules, service accounts, and namespace isolation |
| **Cluster Info** | `ClusterInfo` | Auto-discovers and reports cluster metadata: K8s version, nodes, storage classes, ingress, load balancers |

## Architecture

```
api/                          CRD type definitions (6 API groups, 11 CRDs)
  core/v1alpha1/                Stack, StackResource, ClusterInfo
  addons/v1alpha1/              PostgresCluster
  builds/v1alpha1/              ImageBuild
  registry/v1alpha1/            ClusterRegistry
  storage/v1alpha1/             Volume, NFSServer, ObjectStorage
  users/v1alpha1/               User

internal/controller/          Reconciler implementations
cmd/cluster-agent/            Operator entrypoint
pkg/                          Shared packages
  imagebuilder/                 Kaniko integration
  registry/                     Zot registry builder
  rwmany_provisioner/           Custom NFS dynamic provisioner
  gitsync/                      Git clone into volumes
  volumesync/                   Build artifact sync
  ingresstls/                   TLS certificate management
  config/                       Image refs, constants

config/deploy/                Kubernetes manifests (CRDs, RBAC, namespace)
charts/stackdome-agent/       Helm chart with optional dependencies
test/integration/             Ginkgo integration tests against Kind
```

## Prerequisites

- Go 1.25+
- Docker
- kubectl
- Access to a Kubernetes cluster (v1.28+)

## Getting Started

### Install via Helm

```sh
helm install stackdome-agent charts/stackdome-agent \
  --namespace stackdome-control-plane \
  --create-namespace
```

The chart bundles optional dependencies that can be toggled in `values.yaml`:

| Dependency | Default | Purpose |
|---|---|---|
| `cert-manager` | enabled | TLS certificate management |
| `cloudnative-pg` | enabled | PostgreSQL operator (required for `PostgresCluster` CRs) |
| `plugin-barman-cloud` | enabled | PostgreSQL backup/recovery |
| `traefik` | enabled | Ingress controller |

### Manual Deployment

Build and push the operator image:

```sh
make docker-build docker-push IMG=<registry>/cluster-agent:tag
```

Install CRDs and deploy:

```sh
make install
make deploy IMG=<registry>/cluster-agent:tag
```

### Quick Example

Deploy a service with a health check and public ingress:

```yaml
apiVersion: core.stackdome.io/v1alpha1
kind: StackResource
metadata:
  name: api
spec:
  workloadType: Service
  replicas: 2
  imageSpec:
    image: my-registry/api:latest
  ports:
    - name: http
      number: 8080
      exposeToPublic: true
      fqdn: api.example.com
      tls: true
  environmentVariables:
    - name: DATABASE_URL
      valueFrom:
        secretKeyRef:
          name: db-credentials
          key: url
  healthChecks:
    readiness:
      httpGet:
        path: /healthz
        portName: http
```

The controller creates a Deployment and Service for the StackResource. Use `dependsOn[]` to control ordering between resources — a resource won't proceed until its dependencies report `Available=True`.

## Development

### Build

```sh
make build          # Compile operator binary
make manifests      # Regenerate CRDs and RBAC
make generate       # Run code generators (DeepCopy, etc.)
```

### Test

```sh
make test           # Unit tests with envtest
make test-unit      # Unit tests only
make test-integration   # Full integration suite (requires Docker, ~11 min)
```

The integration test suite spins up a Kind cluster, installs all dependencies (CNPG, cert-manager, S3Mock), deploys the operator, and runs end-to-end tests against real CRs.

### Lint

```sh
make lint           # Run golangci-lint
make lint-fix       # Auto-fix lint issues
```

## CRD Reference

All CRDs use `v1alpha1` and belong to `*.stackdome.io` API groups.

### Stack (`core.stackdome.io`)

A thin grouping shell that references StackResources by name via `resourceNames[]`. Tracks aggregate phase (Pending, Progressing, Ready, Degraded, Failed) and convergence history across its children.

### StackResource (`core.stackdome.io`)

Represents a single workload. Types: `Service`, `StatefulService`, `Worker`, `Job`, `CronJob`. Supports:
- **Image or Build source** — use a pre-built image (`imageSpec`) or build from source (`buildSpec` via Kaniko)
- **Dependency ordering** — `dependsOn[]` ensures resources start in order
- **Health checks** — readiness, liveness, and startup probes (HTTP, TCP, or command)
- **Ports** — named ports with optional public exposure, FQDN, and TLS
- **Environment variables** — literal values or `secretKeyRef`
- **Volume mounts, resource limits, init containers, pre-deploy commands**

### PostgresCluster (`addons.stackdome.io`)

Provisions a CloudNativePG-managed PostgreSQL cluster with:
- Configurable instance count and replicas
- PostgreSQL version selection via ImageCatalog
- WAL archiving and scheduled backups (Barman)
- Multiple databases per cluster
- Extension support (pgvector)

### ClusterRegistry (`registry.stackdome.io`)

Deploys a Zot-based Docker registry inside the cluster with storage management, retention policies, and authentication.

### Volume, NFSServer, ObjectStorage (`storage.stackdome.io`)

- **Volume** — PVCs with optional sync from git repos, remote directories, or build artifacts
- **NFSServer** — Provisions an NFS server for RWMany volumes with a custom dynamic provisioner
- **ObjectStorage** — S3-compatible storage (RustFS) with bucket management and credential rotation

### User (`users.stackdome.io`)

Cluster-scoped user with namespace assignments, RBAC rules, and service account provisioning.

### ClusterInfo (`core.stackdome.io`)

Cluster-scoped singleton that auto-discovers and reports K8s version, node count, storage classes, ingress classes, load balancers, and availability zones.
