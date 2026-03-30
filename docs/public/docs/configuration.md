# Configuring OptimizationTarget

The `OptimizationTarget` CRD is the single configuration object for Kite.  It
is cluster-scoped (no namespace needed) and can target workloads across any
number of namespaces.

## Full spec reference

```yaml
apiVersion: optimization.kite.dev/v1alpha1
kind: OptimizationTarget
metadata:
  name: my-target              # unique name; no namespace
spec:

  # ── Target selector ──────────────────────────────────────────────────────
  target:
    # (Optional) Namespaces to inspect.
    # When empty, all non-system namespaces are included.
    namespaces:
      - production
      - staging

    # (Optional) Namespaces to exclude even when covered by the list above
    # or by the "all namespaces" default.
    excludeNamespaces:
      - kube-system
      - monitoring

    # (Optional) Filter workloads by labels.
    labelSelector:
      matchLabels:
        app.kubernetes.io/part-of: my-platform

    # (Optional) Workload kinds to target.
    # Supported: Deployment, StatefulSet, DaemonSet
    # Default: all three kinds
    kinds:
      - Deployment
      - StatefulSet

  # ── Schedule ─────────────────────────────────────────────────────────────
  # Cron expression (5-field) for when to run a full analysis.
  schedule: "0 2 * * *"       # every day at 02:00 UTC

  # How often to collect metrics from the metrics-server.
  # Default: 5m
  scrapeInterval: 5m

  # ── Algorithm rules ──────────────────────────────────────────────────────
  rules:
    # Percentile of CPU samples to use as baseline (1–100, default 95).
    cpuPercentile: 95

    # Percentile of memory samples (1–100, default 100 = maximum).
    memoryPercentile: 100

    # Safety margin added on top of the baseline (default 15 = 15%).
    cpuSafetyMarginPercent: 15
    memorySafetyMarginPercent: 15

    # Hard minimums — recommendations are never below these values.
    minCPURequest: "10m"
    minMemoryRequest: "32Mi"

    # CPU limit = CPU request × ratio.
    # Omit to remove CPU limits entirely (recommended for most workloads).
    cpuLimitRatio: "2.0"

    # Memory limit = memory request × ratio (default "1.0" → limit = request).
    memoryLimitRatio: "1.3"

    # Lookback window for the algorithm (default 24h).
    historyWindow: 168h        # 7 days

    # How to handle HPA-managed workloads.
    # Skip   – omit from recommendations.
    # Include – include with HPA metadata in the status (default).
    hpaBehavior: Include

  # ── GitOps (optional) ────────────────────────────────────────────────────
  gitOps:
    provider: github           # github | gitlab
    repoURL: "https://github.com/my-org/infra"
    baseBranch: main
    secretRef:
      name: github-token       # Secret must be in kite-system namespace
    pathTemplate: "clusters/prod/{{.Namespace}}/{{.Name}}.yaml"
    prTitleTemplate: "fix(resources): rightsize {{.Kind}} {{.Namespace}}/{{.Name}}"
    prLabels:
      - kite
      - automated
    reviewers:
      - platform-team
    autoMerge: false
    commitMessageTemplate: "fix(resources): apply kite rightsizing for {{.Name}}"

  # ── Dry run ──────────────────────────────────────────────────────────────
  # When true, compute and report recommendations but skip PR creation.
  dryRun: false
```

---

## Target selector

### Selecting specific namespaces

```yaml
target:
  namespaces:
    - production
    - staging
```

### Selecting by label

```yaml
target:
  labelSelector:
    matchLabels:
      kite.dev/optimize: "true"
```

Add the label to any Deployment you want Kite to track:

```bash
kubectl label deployment my-app kite.dev/optimize=true -n production
```

### Excluding namespaces

When `namespaces` is empty Kite auto-discovers all namespaces and skips those
with common system prefixes (`kube-`, `cert-manager`, `istio-`, `monitoring`).
Use `excludeNamespaces` for additional exclusions:

```yaml
target:
  excludeNamespaces:
    - my-special-ns
```

---

## Rules

### Understanding safety margins

The safety margin is the most important tuning knob.  A 15% safety margin means:

```
recommended = baseline × 1.15
```

Start with 15–20% for production workloads.  For batch jobs or workloads with
very spiky traffic, consider 25–30%.

### When to set a CPU limit

Setting `cpuLimitRatio` is optional and controversial:

- **No CPU limit** (omit `cpuLimitRatio`) – pods can burst and use spare node
  capacity.  This is the recommended default for latency-sensitive workloads.
- **CPU limit = 2× request** – a common choice when you want predictable
  throttling behaviour.

Memory limits are always set because an OOM kill is safer than unbounded
memory growth that destabilises other pods.

---

## Path template variables

The `pathTemplate` field accepts [Go text/template](https://pkg.go.dev/text/template)
syntax.  Available variables:

| Variable | Example value |
|----------|---------------|
| `.Namespace` | `production` |
| `.Name` | `my-api` |
| `.Kind` | `Deployment` |

### Example path templates

```
# Simple flat layout
apps/{{.Namespace}}/{{.Name}}.yaml

# Kind-aware layout
manifests/{{.Kind}}/{{.Namespace}}/{{.Name}}/base.yaml

# Cluster-per-directory layout
clusters/prod/{{.Namespace}}/{{.Name}}/kustomization.yaml
```

---

## Observing recommendations

```bash
# Summary view
kubectl get ot my-target

# Full recommendations
kubectl get ot my-target -o jsonpath='{.status.recommendations}' | jq .

# Watch for updates
kubectl get ot my-target -w
```
