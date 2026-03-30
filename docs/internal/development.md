# Development Guide

## Setup

### Prerequisites

- Go 1.23+
- Docker (for image builds)
- kubectl + a running Kubernetes cluster (kind, k3d, or cloud)
- metrics-server installed in the cluster

### Clone and build

```bash
git clone https://github.com/graz-dev/kite.git
cd kite

# Install dependencies
go mod download

# Install code-generation tools locally
make controller-gen envtest

# Regenerate CRD manifests and deepcopy (after editing types)
make generate manifests

# Build binary
make build
```

---

## Running locally against a cluster

```bash
# Point kubectl at your cluster, then:
make install          # installs CRDs
make run              # runs the operator out-of-cluster (no Docker needed)
```

The operator uses `POD_NAMESPACE=kite-system` by default when running locally.
MetricsHistory objects will be created in the `kite-system` namespace (which
must exist).

```bash
kubectl create namespace kite-system
```

---

## Running tests

```bash
make test
```

Tests use [envtest](https://book.kubebuilder.io/reference/envtest.html) which
starts a local API server — no real cluster needed.

---

## Code generation

After editing any type in `api/v1alpha1/`, run:

```bash
make generate   # regenerates zz_generated.deepcopy.go
make manifests  # regenerates CRD YAML and RBAC
```

These targets require `controller-gen` (installed in `./bin/`).

---

## Project layout conventions

| Convention | Detail |
|-----------|--------|
| **No background goroutines** | All timing is handled with `ctrl.Result{RequeueAfter: …}` |
| **Idempotent reconciles** | Every reconcile should be safe to repeat with no side effects |
| **Status-only updates** | The reconciler never modifies the spec |
| **Structured logging** | Use `log.FromContext(ctx).WithValues(…)` everywhere |
| **Dry-run safety** | Check `target.Spec.DryRun` before any write outside the cluster |

---

## Adding a new git provider

1. Create `internal/gitops/mynewprovider.go`.
2. Implement the `Provider` interface:
   ```go
   type Provider interface {
       CreatePR(ctx context.Context, req PRRequest) (*PRResult, error)
   }
   ```
3. Add a `ReadFileFromRepo` method for reading existing manifests.
4. Register the new value in `newGitOpsProvider()` in the controller.
5. Add `mynewprovider` to the CRD validation enum in the YAML manifest and Go types.
6. Update the public docs.

---

## Release process

```bash
# Tag and push
git tag v0.1.0
git push origin v0.1.0
```

The CI workflow will:
1. Build and push a multi-arch Docker image to GHCR.
2. Tag `ghcr.io/graz-dev/kite:v0.1.0` and `ghcr.io/graz-dev/kite:latest`.

---

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `POD_NAMESPACE` | `kite-system` | Namespace where MetricsHistory CRDs are stored |
| `KITE_VERSION` | `dev-local` | Version string injected into logs |

---

## Dependency management

All external dependencies are in `go.mod`.  Key libraries:

| Library | Purpose |
|---------|---------|
| `sigs.k8s.io/controller-runtime` | Operator framework |
| `k8s.io/metrics` | metrics-server API client |
| `github.com/robfig/cron/v3` | Cron expression parser |
| `github.com/go-git/go-git/v5` | In-memory git operations |
| `github.com/google/go-github/v66` | GitHub API |
| `github.com/xanzy/go-gitlab` | GitLab API |
| `sigs.k8s.io/yaml` | YAML ↔ JSON conversion |

---

## Debugging tips

### Inspect MetricsHistory

```bash
# List all history objects
kubectl get mh -n kite-system

# View data for a specific workload
kubectl get mh -n kite-system \
  -l kite.dev/workload-name=my-app \
  -o jsonpath='{.items[0].spec.dataPoints}' | jq 'length'
```

### Force an immediate analysis

```bash
kubectl annotate ot my-target kite.dev/run-now=$(date +%s) --overwrite
```

### Enable verbose logging

```bash
# When running locally:
POD_NAMESPACE=kite-system go run ./cmd/main.go --zap-log-level=debug

# In-cluster (patch the Deployment):
kubectl patch deployment kite-controller-manager -n kite-system \
  --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--zap-log-level=debug"}]'
```
