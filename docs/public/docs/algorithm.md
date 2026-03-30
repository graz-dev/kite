# How the Algorithm Works

## Overview

Kite computes rightsizing recommendations in three phases:

1. **Collect** – Scrape per-pod metrics from the metrics-server and store the per-replica maximum in a `MetricsHistory` CRD.
2. **Aggregate** – On the configured schedule, read the history, filter to the configured time window, and build per-container time series.
3. **Recommend** – Apply the percentile + safety-margin formula and enforce minimums.

---

## Phase 1 — Metrics collection

The Kubernetes metrics-server exposes the
`GET /apis/metrics.k8s.io/v1beta1/namespaces/{ns}/pods` API.  It stores only
the **last ~60 seconds** of data, so Kite must build its own history.

On every scrape interval (default: 5 minutes) Kite:

1. Discovers all pods for each workload matching the `TargetSelector`.
2. Queries the metrics-server for the CPU and memory of each running pod.
3. For each container, records the **maximum** value across all replicas.

!!! tip "Why the maximum?"
    Taking the per-replica maximum is conservative: it ensures we size for the
    hottest replica.  When an HPA scales out and per-pod load drops, future
    samples will naturally reflect the lower per-replica usage.

The observation is appended to the `MetricsHistory` CRD.  Old data points
outside the `historyWindow` are pruned on each write.

---

## Phase 2 — Aggregation

When an analysis run is triggered by the cron schedule, Kite:

1. Reads the `MetricsHistory` for each workload.
2. Discards any data points older than `historyWindow`.
3. For each container, extracts the CPU and memory time series.

### HPA-managed workloads

Because the stored values are already **per-replica figures**, HPA scaling
does not affect the arithmetic: a pod under high load at minimum replica
count and a pod with lower load at maximum replica count both contribute
honest per-pod observations to the series.

When `hpaBehavior: Skip` is set, the workload is excluded entirely.
When `hpaBehavior: Include` (default), the recommendation is computed
normally and annotated with HPA metadata in the status.

---

## Phase 3 — Recommendation formula

For each container:

```
baseline_cpu  = percentile(cpu_samples,  CPUPercentile)
baseline_mem  = percentile(mem_samples, MemoryPercentile)

recommended_cpu_request = max(
    ceil(baseline_cpu  × (1 + CPUSafetyMarginPercent / 100), 10m),
    MinCPURequest
)

recommended_mem_request = max(
    ceil(baseline_mem  × (1 + MemorySafetyMarginPercent / 100), 1Mi),
    MinMemoryRequest
)
```

Limits are optional:

```
# Only if CPULimitRatio is set:
recommended_cpu_limit = recommended_cpu_request × CPULimitRatio

# Always (ratio defaults to 1.0):
recommended_mem_limit = recommended_mem_request × MemoryLimitRatio
```

!!! info "Default values"
    | Parameter | Default |
    |-----------|---------|
    | `cpuPercentile` | 95 |
    | `memoryPercentile` | 100 (maximum) |
    | `cpuSafetyMarginPercent` | 15 |
    | `memorySafetyMarginPercent` | 15 |
    | `minCPURequest` | 10m |
    | `minMemoryRequest` | 32Mi |
    | `memoryLimitRatio` | 1.0 (limit = request) |
    | `historyWindow` | 24h |

---

## Confidence levels

The number of data points collected determines the confidence level
reported in each recommendation:

| Data points | Confidence | Interpretation |
|-------------|------------|----------------|
| 0 | none | No history; current requests are returned unchanged |
| 1–9 | low | Very little data; treat recommendations with caution |
| 10–49 | medium | Several hours of data; reasonable basis |
| 50+ | high | A full day or more; high-quality recommendation |

---

## Why metrics-server instead of Prometheus?

| | metrics-server | Prometheus |
|--|----------------|------------|
| **Cluster requirement** | Bundled with most managed clusters | Separate installation required |
| **Historical data** | Kite builds its own rolling window | Long-term storage built-in |
| **Accuracy** | Based on cAdvisor (same source as Prometheus) | Based on cAdvisor |
| **Setup complexity** | None | Prometheus + kube-state-metrics + cAdvisor |
| **Latency** | ~60 s window at source | Configurable retention |

Kite's approach works out of the box on EKS, GKE, AKS, and any cluster where
`kubectl top` returns data.  If you have Prometheus and want longer history,
you can increase the `historyWindow` and lower the `scrapeInterval` – or
contribute a Prometheus backend (PRs welcome!).
