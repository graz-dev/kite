# CRD Reference

## OptimizationTarget

**Group:** `optimization.kite.dev`
**Version:** `v1alpha1`
**Scope:** Cluster
**Short name:** `ot`

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `target` | `TargetSelector` | ✅ | — | Defines which workloads to analyse |
| `schedule` | `string` | ✅ | — | Cron expression for analysis runs |
| `scrapeInterval` | `Duration` | | `5m` | How often to collect metrics |
| `rules` | `RightsizingRules` | | see below | Algorithm parameters |
| `gitOps` | `GitOpsConfig` | | nil | GitOps PR configuration |
| `dryRun` | `bool` | | `false` | When true, no PRs are created |

#### TargetSelector

| Field | Type | Description |
|-------|------|-------------|
| `namespaces` | `[]string` | Namespaces to include (empty = all non-system) |
| `excludeNamespaces` | `[]string` | Namespaces to exclude |
| `labelSelector` | `LabelSelector` | Filter workloads by labels |
| `kinds` | `[]string` | Workload kinds (default: Deployment, StatefulSet, DaemonSet) |

#### RightsizingRules

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cpuPercentile` | `int32` | `95` | Percentile of CPU samples (1–100) |
| `memoryPercentile` | `int32` | `100` | Percentile of memory samples (1–100) |
| `cpuSafetyMarginPercent` | `int32` | `15` | CPU safety margin % (0–200) |
| `memorySafetyMarginPercent` | `int32` | `15` | Memory safety margin % (0–200) |
| `minCPURequest` | `Quantity` | `10m` | Minimum CPU request |
| `minMemoryRequest` | `Quantity` | `32Mi` | Minimum memory request |
| `cpuLimitRatio` | `*string` | nil (no limit) | CPU limit = request × ratio |
| `memoryLimitRatio` | `*string` | `"1.0"` | Memory limit = request × ratio |
| `historyWindow` | `*Duration` | `24h` | Lookback window for algorithm |
| `hpaBehavior` | `HPABehavior` | `Include` | How to handle HPA workloads |

`HPABehavior` values: `Skip` | `Include`

#### GitOpsConfig

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `provider` | `string` | ✅ | `github` or `gitlab` |
| `repoURL` | `string` | ✅ | HTTPS URL of the GitOps repo |
| `baseBranch` | `string` | | Default: `main` |
| `secretRef` | `LocalObjectReference` | ✅ | Secret with `token` key |
| `pathTemplate` | `string` | ✅ | Go template for manifest file path |
| `prTitleTemplate` | `string` | | Go template for PR title |
| `prBodyTemplate` | `string` | | Go template for PR body (Markdown) |
| `prLabels` | `[]string` | | Labels to add to PRs |
| `autoMerge` | `bool` | | Default: `false` |
| `commitMessageTemplate` | `string` | | Go template for commit message |
| `reviewers` | `[]string` | | GitHub/GitLab usernames |

Template variables available in all `*Template` fields:

| Variable | Type | Example |
|----------|------|---------|
| `.Namespace` | string | `production` |
| `.Name` | string | `my-api` |
| `.Kind` | string | `Deployment` |
| `.Recommendations` | `[]ContainerRec` | see algorithm docs |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | `[]Condition` | Standard Kubernetes conditions |
| `observedGeneration` | `int64` | Last processed generation |
| `lastAnalysisTime` | `*Time` | When the last analysis ran |
| `nextAnalysisTime` | `*Time` | When the next analysis is scheduled |
| `lastScrapeTime` | `*Time` | When metrics were last scraped |
| `totalWorkloads` | `int32` | Number of workloads analysed |
| `workloadsWithPR` | `int32` | Number of PRs opened |
| `recommendations` | `[]WorkloadRecommendation` | Full recommendations list |

#### WorkloadRecommendation

| Field | Type | Description |
|-------|------|-------------|
| `namespace` | `string` | Workload namespace |
| `name` | `string` | Workload name |
| `kind` | `string` | Workload kind |
| `currentReplicas` | `int32` | Observed replica count |
| `hpaManaged` | `bool` | True if an HPA manages this workload |
| `hpaMinReplicas` | `*int32` | HPA minimum replicas |
| `hpaMaxReplicas` | `*int32` | HPA maximum replicas |
| `dataPoints` | `int32` | Metrics samples used |
| `containers` | `[]ContainerRecommendation` | Per-container recommendations |
| `prUrl` | `string` | URL of the opened PR |
| `prStatus` | `string` | PR status (`open`, `error: …`) |
| `generatedAt` | `Time` | When this recommendation was generated |
| `skipped` | `bool` | True if workload was excluded |
| `skipReason` | `string` | Reason for skipping |

#### ContainerRecommendation

| Field | Type | Description |
|-------|------|-------------|
| `name` | `string` | Container name |
| `currentCPURequest` | `string` | Current CPU request |
| `currentCPULimit` | `string` | Current CPU limit |
| `currentMemoryRequest` | `string` | Current memory request |
| `currentMemoryLimit` | `string` | Current memory limit |
| `recommendedCPURequest` | `string` | Recommended CPU request |
| `recommendedCPULimit` | `string` | Recommended CPU limit |
| `recommendedMemoryRequest` | `string` | Recommended memory request |
| `recommendedMemoryLimit` | `string` | Recommended memory limit |
| `cpuRequestDiffPercent` | `float64` | Relative change in CPU request (%) |
| `memoryRequestDiffPercent` | `float64` | Relative change in memory request (%) |
| `confidence` | `string` | `none` / `low` / `medium` / `high` |

---

## MetricsHistory

**Group:** `optimization.kite.dev`
**Version:** `v1alpha1`
**Scope:** Namespaced (stored in the operator's namespace)
**Short name:** `mh`

MetricsHistory is managed automatically by Kite.  You do not need to create
or modify these objects manually.

### Spec

| Field | Type | Description |
|-------|------|-------------|
| `workloadRef` | `WorkloadRef` | Identifies the workload |
| `maxDataPoints` | `int32` | Maximum data points to retain (default 2016) |
| `dataPoints` | `[]MetricsDataPoint` | Time-ordered observations |

#### MetricsDataPoint

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | `Time` | Observation time |
| `replicaCount` | `int32` | Running pods at observation time |
| `containers` | `[]ContainerMetrics` | Per-container max usage |

#### ContainerMetrics

| Field | Type | Description |
|-------|------|-------------|
| `name` | `string` | Container name |
| `cpuMilliCores` | `int64` | Max CPU usage in milli-cores |
| `memoryBytes` | `int64` | Max working-set memory in bytes |
