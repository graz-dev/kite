# Kite — Internal Architecture

## Overview

Kite is a standard Kubernetes operator built with
[controller-runtime v0.19](https://github.com/kubernetes-sigs/controller-runtime).
It manages two CRDs and one reconciler.

```
┌──────────────────────────────────────────────────────────────┐
│  cluster                                                       │
│                                                                │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │  kite-system namespace                                    │ │
│  │                                                           │ │
│  │  ┌────────────────────────────────────────────────────┐  │ │
│  │  │  kite-controller-manager                           │  │ │
│  │  │                                                    │  │ │
│  │  │  ┌─────────────────┐  ┌──────────────────────┐    │  │ │
│  │  │  │  Reconciler     │  │  metrics.Collector   │    │  │ │
│  │  │  │                 │  │                      │    │  │ │
│  │  │  │  • parse cron   │  │  • list workloads    │    │  │ │
│  │  │  │  • decide scrape│  │  • list pods         │    │  │ │
│  │  │  │    or analyse   │  │  • query metrics-srv │    │  │ │
│  │  │  │  • call algo    │  │  • upsert history    │    │  │ │
│  │  │  │  • call gitops  │  └──────────────────────┘    │  │ │
│  │  │  │  • update status│                              │  │ │
│  │  │  │  • requeue      │  ┌──────────────────────┐    │  │ │
│  │  │  └─────────────────┘  │  algorithm.Compute   │    │  │ │
│  │  │                       │                      │    │  │ │
│  │  │                       │  • percentile calc   │    │  │ │
│  │  │                       │  • safety margin     │    │  │ │
│  │  │                       │  • limit ratios      │    │  │ │
│  │  │                       └──────────────────────┘    │  │ │
│  │  │                                                    │  │ │
│  │  │                       ┌──────────────────────┐    │  │ │
│  │  │                       │  gitops.Provider     │    │  │ │
│  │  │                       │  (GitHub/GitLab)     │    │  │ │
│  │  │                       │  • clone             │    │  │ │
│  │  │                       │  • patch manifest    │    │  │ │
│  │  │                       │  • commit + push     │    │  │ │
│  │  │                       │  • create PR         │    │  │ │
│  │  │                       └──────────────────────┘    │  │ │
│  │  └────────────────────────────────────────────────────┘  │ │
│  │                                                           │ │
│  │  MetricsHistory CRDs (one per tracked workload)          │ │
│  └──────────────────────────────────────────────────────────┘ │
│                                                                │
│  OptimizationTarget CRDs (cluster-scoped)                     │
│                                                                │
│  Target workloads (any namespace)                             │
└──────────────────────────────────────────────────────────────┘
```

---

## Package structure

```
kite/
├── api/v1alpha1/
│   ├── groupversion_info.go          scheme registration
│   ├── optimizationtarget_types.go   OptimizationTarget CRD types
│   ├── metricshistory_types.go       MetricsHistory CRD types
│   └── zz_generated.deepcopy.go     generated DeepCopy methods
│
├── cmd/
│   └── main.go                       operator entrypoint, manager setup
│
├── internal/
│   ├── controller/
│   │   └── optimizationtarget_controller.go  main reconciler
│   ├── metrics/
│   │   └── collector.go              metrics-server scraper + history manager
│   ├── algorithm/
│   │   └── rightsizing.go            percentile + safety-margin recommendation engine
│   └── gitops/
│       ├── provider.go               Provider interface, template helpers, YAML patcher
│       ├── github.go                 GitHubProvider implementation
│       └── gitlab.go                 GitLabProvider implementation
│
├── config/
│   ├── crd/bases/                    CRD YAML manifests
│   ├── rbac/                         ClusterRole, ClusterRoleBinding, ServiceAccount
│   ├── manager/                      Deployment + Service
│   ├── default/                      Kustomize root
│   └── samples/                      Example OptimizationTarget objects
│
├── docs/
│   ├── internal/                     This documentation
│   └── public/                       MkDocs Material site (GitHub Pages)
│
└── .github/workflows/
    ├── ci.yaml                       lint + test + build + release
    └── docs.yaml                     deploy MkDocs to GitHub Pages
```

---

## Reconcile loop design

The reconciler uses the **RequeueAfter** pattern instead of background
goroutines.  This is simpler, more testable, and aligns with controller-runtime
best practices.

```
Reconcile(ctx, req)
  │
  ├─ Get OptimizationTarget (return if NotFound)
  ├─ Handle deletion (remove finalizer)
  ├─ Ensure finalizer is present
  ├─ Parse cron schedule → fail-fast on invalid expression
  │
  ├─ Calculate nextScrapeTime  = lastScrapeTime + scrapeInterval
  ├─ Calculate nextAnalysisTime = cron.Next(lastAnalysisTime)
  │
  ├─ if now ≥ nextScrapeTime:
  │    └─ collector.ScrapeAndPersist(ctx, target)
  │         ├─ Resolve namespaces
  │         ├─ For each namespace × kind:
  │         │    ├─ List workloads
  │         │    ├─ Check HPA membership
  │         │    ├─ List running pods
  │         │    ├─ Query metrics-server (per pod)
  │         │    ├─ Aggregate → per-replica max per container
  │         │    └─ Upsert MetricsHistory CRD (prune old points)
  │         └─ return []WorkloadSummary
  │
  ├─ if now ≥ nextAnalysisTime:
  │    └─ for each WorkloadSummary:
  │         ├─ collector.GetHistory(ns, name, kind)
  │         ├─ discoverContainers(ns, name, kind)
  │         ├─ algorithm.Compute(input, rules)
  │         └─ if gitops configured: gitops.Provider.CreatePR(...)
  │
  ├─ Update status
  └─ Return RequeueAfter = min(nextScrape, nextAnalysis) - now
```

---

## CRD lifecycle

### OptimizationTarget

- Cluster-scoped (no namespace in the name).
- Users create and manage these directly.
- The operator adds a finalizer on first reconcile and removes it on deletion.
- Status is updated after every scrape and analysis run.

### MetricsHistory

- Namespace-scoped, stored in the **operator's namespace** (`kite-system`).
- Named: `{kind-lowercase}-{namespace}-{name}` (hashed if > 63 chars).
- One object per tracked workload.
- Created automatically; users should not modify them.
- Labelled with `app.kubernetes.io/managed-by=kite` and workload identity labels.
- Old data points are pruned on each write; objects are NOT deleted when an
  `OptimizationTarget` is deleted (the history may be useful for the next run).

---

## Metrics-server limitations and mitigations

| Limitation | Mitigation |
|------------|------------|
| Only stores ~60s of data | Kite scrapes periodically and persists to CRDs |
| Data may be unavailable during node churn | Missed scrapes are silently skipped |
| Pod restart resets usage counters | No special handling; first samples after restart may be low |
| No namespace-level aggregation | Kite aggregates per-workload using pod selectors |

---

## GitOps flow

The GitOps path uses **in-memory git operations** (go-git + memory filesystem)
to avoid writing to disk.  The full clone is depth=1 for speed.

```
ReadFileFromRepo (GitHub/GitLab Contents API)
  └─ Returns current YAML content of the manifest

UpdateResourcesInManifest(content, recs)
  ├─ yaml.Unmarshal → generic map
  ├─ Navigate to spec.template.spec.containers
  ├─ For each container matching a recommendation:
  │    └─ Overwrite resources.requests.{cpu,memory}
  │    └─ Overwrite resources.limits.{cpu,memory} (if configured)
  └─ Marshal back to YAML

gitops.Provider.CreatePR
  ├─ Clone repo into memory (go-git + memfs)
  ├─ Checkout new branch
  ├─ Write patched file to in-memory filesystem
  ├─ Stage + commit
  ├─ Push to remote
  └─ GitHub/GitLab API → create PR
```
