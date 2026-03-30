# Getting Started

## Prerequisites

| Requirement | Minimum version | Notes |
|-------------|-----------------|-------|
| Kubernetes | 1.26+ | |
| metrics-server | 0.6+ | Must be installed and reachable |
| kubectl | 1.26+ | |
| (Optional) kustomize | 5.0+ | For the default deployment |

### Verify metrics-server

```bash
kubectl top nodes
kubectl top pods -A
```

If either command fails, install or enable the metrics-server for your cluster
([official installation guide](https://github.com/kubernetes-sigs/metrics-server#installation)).

---

## Installation

### Option 1 – release assets from GHCR (recommended for production)

Every [GitHub Release](https://github.com/graz-dev/kite/releases) publishes:

- A multi-arch Docker image to `ghcr.io/graz-dev/kite:<tag>` (amd64 + arm64)
- CRD YAML files as release assets

```bash
VERSION=v0.1.0   # pin to a specific release

# CRDs (from release assets)
kubectl apply -f https://github.com/graz-dev/kite/releases/download/${VERSION}/optimization.kite.dev_optimizationtargets.yaml
kubectl apply -f https://github.com/graz-dev/kite/releases/download/${VERSION}/optimization.kite.dev_metricshistories.yaml

# RBAC + Deployment (image pinned via kustomize override)
kubectl create namespace kite-system --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -k https://github.com/graz-dev/kite/config/default/?ref=${VERSION}
kubectl set image deployment/kite-controller-manager \
  manager=ghcr.io/graz-dev/kite:${VERSION} \
  -n kite-system
```

Available image tags on GHCR:

| Tag | Meaning |
|-----|---------|
| `ghcr.io/graz-dev/kite:latest` | Most recent release |
| `ghcr.io/graz-dev/kite:v0.1.0` | Exact release (recommended) |
| `ghcr.io/graz-dev/kite:0.1` | Latest patch of 0.1.x |

### Option 2 – main branch (for evaluation)

```bash
kubectl create namespace kite-system --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -k https://github.com/graz-dev/kite/config/default/?ref=main
```

### Option 3 – build from source

```bash
git clone https://github.com/graz-dev/kite.git
cd kite
make install deploy IMG=ghcr.io/graz-dev/kite:latest
```

---

## Verify the installation

```bash
# The operator pod should be Running
kubectl get pods -n kite-system

# CRDs should be registered
kubectl get crd | grep kite
# optimization.kite.dev   optimizationtargets   ...
# optimization.kite.dev   metricshistories      ...
```

---

## Your first OptimizationTarget (report-only)

Create a simple target that analyses the `default` namespace and writes
recommendations to the status — no GitOps integration, no repository access
needed.

```yaml title="my-first-target.yaml"
apiVersion: optimization.kite.dev/v1alpha1
kind: OptimizationTarget
metadata:
  name: default-rightsizing
spec:
  target:
    namespaces:
      - default
  schedule: "0 * * * *"   # every hour
  scrapeInterval: 5m
  rules:
    cpuPercentile: 95
    memoryPercentile: 100
    cpuSafetyMarginPercent: 15
    memorySafetyMarginPercent: 15
    historyWindow: 2h
  dryRun: true
```

```bash
kubectl apply -f my-first-target.yaml
```

Wait for at least one scrape interval and one analysis run, then inspect the
status:

```bash
kubectl get ot default-rightsizing -o yaml
```

The `status.recommendations` section will list per-workload, per-container
recommendations:

```yaml
status:
  lastAnalysisTime: "2025-03-30T02:00:00Z"
  totalWorkloads: 3
  recommendations:
    - namespace: default
      name: my-app
      kind: Deployment
      currentReplicas: 2
      dataPoints: 24
      containers:
        - name: app
          currentCPURequest: "200m"
          recommendedCPURequest: "80m"
          cpuRequestDiffPercent: -60.0
          confidence: medium
```

---

## Next steps

- [Configure GitOps integration →](gitops.md)
- [Understand the algorithm →](algorithm.md)
- [Full CRD reference →](crd-reference.md)
