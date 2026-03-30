// Package algorithm implements the resource rightsizing recommendation engine.
//
// # Overview
//
// For each container in a workload the engine collects all CPU and memory
// samples stored in the corresponding MetricsHistory and computes:
//
//   - CPU request  = percentile(cpu_samples, CPUPercentile)  * (1 + CPUSafetyMarginPercent/100)
//   - Mem request  = percentile(mem_samples, MemoryPercentile) * (1 + MemorySafetyMarginPercent/100)
//
// Both results are clipped to the configured minimum values and rounded up to
// human-friendly multiples (10 m for CPU, 1 Mi for memory).
//
// # HPA-aware logic
//
// Because the metrics stored in MetricsHistory are already per-replica figures
// (the maximum across all running pods at each observation), HPA scaling does
// not require special arithmetic – the per-pod sample naturally reflects the
// load at any replica count.  If HPABehavior is Skip the workload is excluded
// from recommendations.
package algorithm

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	optimizationv1alpha1 "github.com/graz-dev/kite/api/v1alpha1"
)

// ContainerInput describes a container's current resource configuration.
type ContainerInput struct {
	Name                string
	CurrentCPURequest   resource.Quantity
	CurrentCPULimit     *resource.Quantity
	CurrentMemoryRequest resource.Quantity
	CurrentMemoryLimit  *resource.Quantity
}

// WorkloadInput bundles everything the algorithm needs to produce a
// recommendation for one workload.
type WorkloadInput struct {
	Namespace       string
	Name            string
	Kind            string
	CurrentReplicas int32
	HPAManaged      bool
	HPAMinReplicas  *int32
	HPAMaxReplicas  *int32
	Containers      []ContainerInput
	History         *optimizationv1alpha1.MetricsHistory
}

// ContainerRec is the recommendation for one container.
type ContainerRec struct {
	Name                     string
	RecommendedCPURequest    resource.Quantity
	RecommendedCPULimit      *resource.Quantity
	RecommendedMemoryRequest resource.Quantity
	RecommendedMemoryLimit   *resource.Quantity
	CPURequestDiffPercent    float64
	MemoryRequestDiffPercent float64
}

// Recommendation is the output of the engine for one workload.
type Recommendation struct {
	Containers []ContainerRec
	DataPoints int32
	// Confidence is none|low|medium|high based on the number of data points.
	Confidence string
	Skipped    bool
	SkipReason string
}

// defaultRules returns a RightsizingRules value with the built-in defaults
// applied to any zero-valued fields.
func defaultRules(r optimizationv1alpha1.RightsizingRules) optimizationv1alpha1.RightsizingRules {
	if r.CPUPercentile == 0 {
		r.CPUPercentile = 95
	}
	if r.MemoryPercentile == 0 {
		r.MemoryPercentile = 100
	}
	if r.CPUSafetyMarginPercent == 0 {
		r.CPUSafetyMarginPercent = 15
	}
	if r.MemorySafetyMarginPercent == 0 {
		r.MemorySafetyMarginPercent = 15
	}
	if r.MinCPURequest.IsZero() {
		r.MinCPURequest = resource.MustParse("10m")
	}
	if r.MinMemoryRequest.IsZero() {
		r.MinMemoryRequest = resource.MustParse("32Mi")
	}
	if r.HPABehavior == "" {
		r.HPABehavior = optimizationv1alpha1.HPABehaviorInclude
	}
	return r
}

// Compute produces a Recommendation for the given workload input and rules.
func Compute(input WorkloadInput, rules optimizationv1alpha1.RightsizingRules) Recommendation {
	rules = defaultRules(rules)

	// Handle HPA skip behaviour.
	if input.HPAManaged && rules.HPABehavior == optimizationv1alpha1.HPABehaviorSkip {
		return Recommendation{
			Skipped:    true,
			SkipReason: "workload is HPA-managed and HPABehavior is Skip",
		}
	}

	// Build per-container sample series from the history.
	type series struct {
		cpu []float64 // milli-cores
		mem []float64 // bytes
	}
	containerSeries := make(map[string]*series)

	var historyWindow time.Duration
	if rules.HistoryWindow != nil {
		historyWindow = rules.HistoryWindow.Duration
	} else {
		historyWindow = 24 * time.Hour
	}
	cutoff := time.Now().Add(-historyWindow)

	if input.History != nil {
		for _, dp := range input.History.Spec.DataPoints {
			if !dp.Timestamp.Time.After(cutoff) {
				continue
			}
			for _, cm := range dp.Containers {
				s, ok := containerSeries[cm.Name]
				if !ok {
					s = &series{}
					containerSeries[cm.Name] = s
				}
				s.cpu = append(s.cpu, float64(cm.CPUMilliCores))
				s.mem = append(s.mem, float64(cm.MemoryBytes))
			}
		}
	}

	// Count total data points (use the container with the most samples).
	totalDP := 0
	for _, s := range containerSeries {
		if len(s.cpu) > totalDP {
			totalDP = len(s.cpu)
		}
	}
	confidence := confidenceLevel(totalDP)

	var recs []ContainerRec

	for _, ci := range input.Containers {
		s := containerSeries[ci.Name]

		var recCPUMilli, recMemBytes float64

		if s == nil || len(s.cpu) == 0 {
			// No data: fall back to current requests as a safe default.
			recCPUMilli = float64(ci.CurrentCPURequest.MilliValue())
			recMemBytes = float64(ci.CurrentMemoryRequest.Value())
		} else {
			recCPUMilli = percentile(s.cpu, float64(rules.CPUPercentile))
			recMemBytes = percentile(s.mem, float64(rules.MemoryPercentile))
		}

		// Apply safety margins.
		recCPUMilli *= 1 + float64(rules.CPUSafetyMarginPercent)/100
		recMemBytes *= 1 + float64(rules.MemorySafetyMarginPercent)/100

		// Round up to friendly multiples.
		recCPUMilli = ceilToMultiple(recCPUMilli, 10)  // nearest 10 m
		recMemBytes = ceilToMultiple(recMemBytes, float64(1<<20)) // nearest 1 Mi

		// Enforce minimums.
		minCPUMilli := float64(rules.MinCPURequest.MilliValue())
		if recCPUMilli < minCPUMilli {
			recCPUMilli = minCPUMilli
		}
		minMemBytes := float64(rules.MinMemoryRequest.Value())
		if recMemBytes < minMemBytes {
			recMemBytes = minMemBytes
		}

		cpuReq := milliCoresToQuantity(recCPUMilli)
		memReq := bytesToQuantity(recMemBytes)

		var cpuLim, memLim *resource.Quantity

		if rules.CPULimitRatio != nil {
			ratio, err := strconv.ParseFloat(*rules.CPULimitRatio, 64)
			if err == nil && ratio > 0 {
				v := milliCoresToQuantity(recCPUMilli * ratio)
				cpuLim = &v
			}
		}

		// Memory limit defaults to 1.0x (limit == request) unless overridden.
		memRatio := 1.0
		if rules.MemoryLimitRatio != nil {
			if r, err := strconv.ParseFloat(*rules.MemoryLimitRatio, 64); err == nil && r > 0 {
				memRatio = r
			}
		}
		v := bytesToQuantity(recMemBytes * memRatio)
		memLim = &v

		// Compute diff percentages.
		cpuDiff := diffPercent(float64(ci.CurrentCPURequest.MilliValue()), recCPUMilli)
		memDiff := diffPercent(float64(ci.CurrentMemoryRequest.Value()), recMemBytes)

		recs = append(recs, ContainerRec{
			Name:                     ci.Name,
			RecommendedCPURequest:    cpuReq,
			RecommendedCPULimit:      cpuLim,
			RecommendedMemoryRequest: memReq,
			RecommendedMemoryLimit:   memLim,
			CPURequestDiffPercent:    cpuDiff,
			MemoryRequestDiffPercent: memDiff,
		})
	}

	return Recommendation{
		Containers: recs,
		DataPoints: int32(totalDP),
		Confidence: confidence,
	}
}

// --- helper functions --------------------------------------------------------

// percentile returns the p-th percentile (0 < p ≤ 100) of a float64 slice.
// The slice is sorted in-place for efficiency.
func percentile(data []float64, p float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sort.Float64s(data)
	if p >= 100 {
		return data[len(data)-1]
	}
	idx := (p / 100) * float64(len(data)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return data[lo]
	}
	frac := idx - float64(lo)
	return data[lo]*(1-frac) + data[hi]*frac
}

// ceilToMultiple rounds v up to the nearest multiple of m.
func ceilToMultiple(v, m float64) float64 {
	if m == 0 {
		return v
	}
	return math.Ceil(v/m) * m
}

// milliCoresToQuantity converts milli-cores to a resource.Quantity string like "120m".
func milliCoresToQuantity(milliCores float64) resource.Quantity {
	return *resource.NewMilliQuantity(int64(milliCores), resource.DecimalSI)
}

// bytesToQuantity converts bytes to a resource.Quantity in Mi.
func bytesToQuantity(bytes float64) resource.Quantity {
	mib := int64(bytes) / (1 << 20)
	return resource.MustParse(fmt.Sprintf("%dMi", mib))
}

// diffPercent returns the relative change from old to new as a percentage.
func diffPercent(old, new float64) float64 {
	if old == 0 {
		return 0
	}
	return (new - old) / old * 100
}

// confidenceLevel maps a sample count to a human-readable confidence label.
func confidenceLevel(n int) string {
	switch {
	case n == 0:
		return "none"
	case n < 10:
		return "low"
	case n < 50:
		return "medium"
	default:
		return "high"
	}
}
