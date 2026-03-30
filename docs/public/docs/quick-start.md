# Quick Start

This page walks through a complete end-to-end example in under 5 minutes.

## Prerequisites

- A running Kubernetes cluster with `kubectl` access
- `metrics-server` installed (`kubectl top nodes` must return data)
- (Optional) `kustomize` v5+ for the kustomize-based install

---

## 1. Install Kite

Choose the method that fits your workflow.

### Option A — specific release (recommended for production)

```bash
VERSION=v0.1.0   # replace with the latest release tag

# CRDs
kubectl apply -f https://github.com/graz-dev/kite/releases/download/${VERSION}/optimization.kite.dev_optimizationtargets.yaml
kubectl apply -f https://github.com/graz-dev/kite/releases/download/${VERSION}/optimization.kite.dev_metricshistories.yaml

# Namespace + RBAC + Deployment (pinned to the release image)
kubectl create namespace kite-system --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -k https://github.com/graz-dev/kite/config/default/?ref=${VERSION}

# Override the image tag to match the release
kubectl set image deployment/kite-controller-manager \
  manager=ghcr.io/graz-dev/kite:${VERSION} \
  -n kite-system
```

### Option B — latest from main (for testing / evaluation)

```bash
kubectl create namespace kite-system --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -k https://github.com/graz-dev/kite/config/default/?ref=main
```

!!! note "Image availability"
    The `ghcr.io/graz-dev/kite` image is published to the
    [GitHub Container Registry](https://github.com/graz-dev/kite/pkgs/container/kite)
    on every GitHub Release.  If your cluster is air-gapped, mirror it first:
    ```bash
    docker pull ghcr.io/graz-dev/kite:v0.1.0
    docker tag  ghcr.io/graz-dev/kite:v0.1.0 your-registry/kite:v0.1.0
    docker push your-registry/kite:v0.1.0
    ```

### Verify

```bash
kubectl -n kite-system rollout status deploy/kite-controller-manager
# Waiting for deployment "kite-controller-manager" rollout to finish: 0 of 1 updated replicas are available...
# deployment "kite-controller-manager" successfully rolled out

kubectl -n kite-system get pods
# NAME                                       READY   STATUS    RESTARTS   AGE
# kite-controller-manager-7d9b6c4f8b-xkpt9   1/1     Running   0          30s
```

---

## 2. Create a report-only target

```bash
kubectl apply -f - <<'EOF'
apiVersion: optimization.kite.dev/v1alpha1
kind: OptimizationTarget
metadata:
  name: quick-start
spec:
  target:
    namespaces: [default]
  schedule: "*/10 * * * *"   # every 10 minutes (demo — use "0 2 * * *" in production)
  scrapeInterval: 1m
  rules:
    historyWindow: 10m
  dryRun: true
EOF
```

---

## 3. Wait for the first analysis

```bash
# Watch the status columns update
kubectl get ot quick-start -w
```

After the first analysis:

```
NAME          SCHEDULE          LAST ANALYSIS          WORKLOADS   PRS OPENED
quick-start   */10 * * * *      2025-03-30T10:10:00Z   5           0
```

---

## 4. Inspect recommendations

```bash
kubectl get ot quick-start -o jsonpath='{.status.recommendations}' | jq .
```

Example output:

```json
[
  {
    "namespace": "default",
    "name": "my-app",
    "kind": "Deployment",
    "currentReplicas": 2,
    "dataPoints": 10,
    "containers": [
      {
        "name": "app",
        "currentCPURequest": "200m",
        "recommendedCPURequest": "80m",
        "cpuRequestDiffPercent": -60.0,
        "currentMemoryRequest": "256Mi",
        "recommendedMemoryRequest": "96Mi",
        "memoryRequestDiffPercent": -62.5,
        "confidence": "low"
      }
    ]
  }
]
```

---

## 5. Enable GitOps (optional)

```bash
# Create the GitHub token secret in kite-system
kubectl create secret generic github-token \
  --from-literal=token=ghp_YOUR_TOKEN \
  -n kite-system

# Update the target to enable PR creation
kubectl patch ot quick-start --type=merge -p '{
  "spec": {
    "dryRun": false,
    "gitOps": {
      "provider": "github",
      "repoURL": "https://github.com/my-org/my-infra",
      "secretRef": {"name": "github-token"},
      "pathTemplate": "apps/{{.Namespace}}/{{.Name}}/deployment.yaml"
    }
  }
}'
```

On the next analysis run Kite will open one pull request per workload that has
a meaningful recommendation.

---

## 6. Upgrade Kite

```bash
NEW_VERSION=v0.2.0

kubectl set image deployment/kite-controller-manager \
  manager=ghcr.io/graz-dev/kite:${NEW_VERSION} \
  -n kite-system

kubectl -n kite-system rollout status deploy/kite-controller-manager
```

If the new release includes CRD changes, apply the updated manifests first:

```bash
kubectl apply -f https://github.com/graz-dev/kite/releases/download/${NEW_VERSION}/optimization.kite.dev_optimizationtargets.yaml
kubectl apply -f https://github.com/graz-dev/kite/releases/download/${NEW_VERSION}/optimization.kite.dev_metricshistories.yaml
```
