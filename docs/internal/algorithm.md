# Algorithm — Internal Reference

## Input

```go
type WorkloadInput struct {
    Namespace       string
    Name            string
    Kind            string
    CurrentReplicas int32
    HPAManaged      bool
    HPAMinReplicas  *int32
    HPAMaxReplicas  *int32
    Containers      []ContainerInput   // current requests/limits
    History         *MetricsHistory    // nil if no data collected yet
}
```

## Algorithm steps

```
1. Apply rule defaults (fill zero values with built-in defaults)

2. Handle HPA skip
   if HPAManaged && HPABehavior == Skip:
       return Recommendation{Skipped: true}

3. Build per-container time series
   for each DataPoint in History.Spec.DataPoints:
       if DataPoint.Timestamp > now - historyWindow:
           for each ContainerMetrics:
               cpu_series[container] += CPUMilliCores
               mem_series[container] += MemoryBytes

4. For each container:
   a. baseline_cpu  = percentile(cpu_series[c], CPUPercentile)
   b. baseline_mem  = percentile(mem_series[c], MemoryPercentile)

   c. rec_cpu  = ceil(baseline_cpu  × (1 + CPUSafetyMarginPercent/100), 10)
   d. rec_mem  = ceil(baseline_mem  × (1 + MemorySafetyMarginPercent/100), 1Mi)

   e. rec_cpu  = max(rec_cpu,  MinCPURequest.MilliValue())
   f. rec_mem  = max(rec_mem,  MinMemoryRequest.Value())

   g. cpu_limit = rec_cpu × CPULimitRatio     (if CPULimitRatio set)
   h. mem_limit = rec_mem × MemoryLimitRatio  (default ratio 1.0)

   i. cpu_diff% = (rec_cpu  - current_cpu_req) / current_cpu_req × 100
   j. mem_diff% = (rec_mem  - current_mem_req) / current_mem_req × 100

5. Confidence
   n = len(cpu_series[any_container])
   if n == 0: "none"
   if n <  10: "low"
   if n <  50: "medium"
   else:       "high"
```

## Percentile implementation

The percentile function sorts the slice and interpolates linearly between
adjacent elements:

```go
func percentile(data []float64, p float64) float64 {
    sort.Float64s(data)
    idx := (p / 100) * float64(len(data)-1)
    lo, hi := floor(idx), ceil(idx)
    if lo == hi { return data[lo] }
    frac := idx - float64(lo)
    return data[lo]*(1-frac) + data[hi]*frac
}
```

## Rounding

Values are rounded up to avoid under-provisioning:

- **CPU**: rounded up to the nearest 10 milli-cores (e.g. 83 m → 90 m)
- **Memory**: rounded up to the nearest mebibyte (e.g. 68.3 Mi → 69 Mi)

This keeps resource strings clean and avoids unnecessary precision.

## HPA-aware design rationale

The data stored in `MetricsHistory` is the **maximum per-replica usage** at
each observation:

```
scrape time T:  pod-0 uses 50m, pod-1 uses 80m, pod-2 uses 60m
stored value:   max(50, 80, 60) = 80m
```

This means:
- At low scale (e.g. 2 replicas), per-pod load is higher → stored values
  are higher → recommendations are appropriately sized.
- At high scale (e.g. 10 replicas), per-pod load is lower → stored values
  are lower → future recommendations will naturally drop.
- The algorithm never needs to divide by replica count.

The only caveat is burst events where the HPA hasn't yet scaled out:
if a workload spikes briefly before the HPA reacts, the high per-replica
usage will be captured and appropriately included in the percentile
calculation.
