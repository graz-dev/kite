// Package metrics handles collection of resource-usage data from the
// Kubernetes metrics-server and persistence of that data into MetricsHistory
// CRD objects.
//
// Because the metrics-server only retains a very short sliding window (~60 s)
// of pod metrics, Kite scrapes the API on a configurable interval (default
// 5 minutes) and stores the observations itself so that the rightsizing
// algorithm has a meaningful lookback window (default 24 h).
package metrics

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	optimizationv1alpha1 "github.com/graz-dev/kite/api/v1alpha1"
)

const (
	// DefaultScrapeInterval is used when OptimizationTargetSpec.ScrapeInterval is nil.
	DefaultScrapeInterval = 5 * time.Minute

	// DefaultMaxDataPoints is the default cap for MetricsHistory.Spec.MaxDataPoints.
	// 2016 points ≈ 7 days at 5-minute intervals.
	DefaultMaxDataPoints = 2016

	// DefaultHistoryWindow is the default lookback duration for the algorithm.
	DefaultHistoryWindow = 24 * time.Hour
)

// WorkloadSummary is a lightweight description of a discovered workload with
// its HPA status – used by the controller to iterate across workloads.
type WorkloadSummary struct {
	Namespace       string
	Name            string
	Kind            string
	CurrentReplicas int32
	HPAManaged      bool
	HPAMinReplicas  *int32
	HPAMaxReplicas  *int32
}

// Collector wraps the Kubernetes API clients needed for metrics collection.
type Collector struct {
	Client            client.Client
	MetricsClient     metricsclient.Interface
	OperatorNamespace string
}

// ScrapeAndPersist discovers all workloads that are in scope for the given
// OptimizationTarget, queries the metrics-server for their pod metrics, and
// upserts a MetricsHistory CRD for each workload.
//
// It also prunes data points that fall outside the configured history window.
func (c *Collector) ScrapeAndPersist(ctx context.Context, target *optimizationv1alpha1.OptimizationTarget) ([]WorkloadSummary, error) {
	logger := log.FromContext(ctx).WithValues("optimizationtarget", target.Name)

	historyWindow := DefaultHistoryWindow
	if target.Spec.Rules.HistoryWindow != nil {
		historyWindow = target.Spec.Rules.HistoryWindow.Duration
	}

	maxDataPoints := int32(DefaultMaxDataPoints)

	namespaces, err := c.resolveNamespaces(ctx, target.Spec.Target)
	if err != nil {
		return nil, fmt.Errorf("resolving namespaces: %w", err)
	}

	kinds := target.Spec.Target.Kinds
	if len(kinds) == 0 {
		kinds = []string{"Deployment", "StatefulSet", "DaemonSet"}
	}

	var summaries []WorkloadSummary

	for _, ns := range namespaces {
		for _, kind := range kinds {
			ws, err := c.scrapeNamespaceKind(ctx, ns, kind, target.Spec.Target.LabelSelector, historyWindow, maxDataPoints)
			if err != nil {
				logger.Error(err, "Failed to scrape workloads", "namespace", ns, "kind", kind)
				continue
			}
			summaries = append(summaries, ws...)
		}
	}

	return summaries, nil
}

// scrapeNamespaceKind handles one (namespace, kind) combination.
func (c *Collector) scrapeNamespaceKind(
	ctx context.Context,
	namespace, kind string,
	labelSel *metav1.LabelSelector,
	historyWindow time.Duration,
	maxDataPoints int32,
) ([]WorkloadSummary, error) {
	logger := log.FromContext(ctx)

	workloads, err := c.listWorkloads(ctx, namespace, kind, labelSel)
	if err != nil {
		return nil, err
	}

	// Build a map of HPA targets so we can annotate workloads.
	hpaMap, err := c.buildHPAMap(ctx, namespace)
	if err != nil {
		logger.Error(err, "Failed to list HPAs; HPA metadata will be missing", "namespace", namespace)
		hpaMap = map[string]*autoscalingv2.HorizontalPodAutoscaler{}
	}

	var summaries []WorkloadSummary

	for _, wl := range workloads {
		wlName := wl["name"].(string)
		podSelector := wl["selector"].(labels.Selector)
		replicas := wl["replicas"].(int32)

		// Resolve HPA info.
		hpaKey := kind + "/" + wlName
		hpa, hpaManaged := hpaMap[hpaKey]
		var hpaMin, hpaMax *int32
		if hpaManaged {
			hpaMin = hpa.Spec.MinReplicas
			max := hpa.Spec.MaxReplicas
			hpaMax = &max
		}

		// List pods for this workload.
		podList := &corev1.PodList{}
		if err := c.Client.List(ctx, podList,
			client.InNamespace(namespace),
			client.MatchingLabelsSelector{Selector: podSelector},
		); err != nil {
			logger.Error(err, "Failed to list pods", "workload", wlName)
			continue
		}

		runningPods := filterRunningPods(podList.Items)
		if len(runningPods) == 0 {
			continue
		}

		// Query metrics-server for each running pod and aggregate per-replica.
		containerMetrics, err := c.aggregatePodMetrics(ctx, namespace, runningPods)
		if err != nil {
			logger.Error(err, "Failed to get pod metrics", "workload", wlName)
			continue
		}
		if len(containerMetrics) == 0 {
			continue
		}

		dataPoint := optimizationv1alpha1.MetricsDataPoint{
			Timestamp:    metav1.Now(),
			ReplicaCount: replicas,
			Containers:   containerMetrics,
		}

		if err := c.upsertMetricsHistory(ctx, namespace, wlName, kind, dataPoint, historyWindow, maxDataPoints); err != nil {
			logger.Error(err, "Failed to persist MetricsHistory", "workload", wlName)
		}

		summaries = append(summaries, WorkloadSummary{
			Namespace:       namespace,
			Name:            wlName,
			Kind:            kind,
			CurrentReplicas: replicas,
			HPAManaged:      hpaManaged,
			HPAMinReplicas:  hpaMin,
			HPAMaxReplicas:  hpaMax,
		})
	}

	return summaries, nil
}

// listWorkloads returns a slice of maps with keys: name, selector, replicas.
func (c *Collector) listWorkloads(
	ctx context.Context,
	namespace, kind string,
	labelSel *metav1.LabelSelector,
) ([]map[string]interface{}, error) {
	var listOpts []client.ListOption
	listOpts = append(listOpts, client.InNamespace(namespace))
	if labelSel != nil {
		sel, err := metav1.LabelSelectorAsSelector(labelSel)
		if err != nil {
			return nil, fmt.Errorf("parsing label selector: %w", err)
		}
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: sel})
	}

	var results []map[string]interface{}

	switch kind {
	case "Deployment":
		list := &appsv1.DeploymentList{}
		if err := c.Client.List(ctx, list, listOpts...); err != nil {
			return nil, err
		}
		for i := range list.Items {
			d := &list.Items[i]
			sel, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
			if err != nil {
				continue
			}
			replicas := int32(1)
			if d.Spec.Replicas != nil {
				replicas = *d.Spec.Replicas
			}
			results = append(results, map[string]interface{}{
				"name":     d.Name,
				"selector": sel,
				"replicas": replicas,
			})
		}

	case "StatefulSet":
		list := &appsv1.StatefulSetList{}
		if err := c.Client.List(ctx, list, listOpts...); err != nil {
			return nil, err
		}
		for i := range list.Items {
			s := &list.Items[i]
			sel, err := metav1.LabelSelectorAsSelector(s.Spec.Selector)
			if err != nil {
				continue
			}
			replicas := int32(1)
			if s.Spec.Replicas != nil {
				replicas = *s.Spec.Replicas
			}
			results = append(results, map[string]interface{}{
				"name":     s.Name,
				"selector": sel,
				"replicas": replicas,
			})
		}

	case "DaemonSet":
		list := &appsv1.DaemonSetList{}
		if err := c.Client.List(ctx, list, listOpts...); err != nil {
			return nil, err
		}
		for i := range list.Items {
			d := &list.Items[i]
			sel, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
			if err != nil {
				continue
			}
			results = append(results, map[string]interface{}{
				"name":     d.Name,
				"selector": sel,
				"replicas": d.Status.NumberReady,
			})
		}
	}

	return results, nil
}

// buildHPAMap returns a map of "<Kind>/<name>" -> HPA for the given namespace.
func (c *Collector) buildHPAMap(ctx context.Context, namespace string) (map[string]*autoscalingv2.HorizontalPodAutoscaler, error) {
	list := &autoscalingv2.HorizontalPodAutoscalerList{}
	if err := c.Client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	m := make(map[string]*autoscalingv2.HorizontalPodAutoscaler, len(list.Items))
	for i := range list.Items {
		hpa := &list.Items[i]
		ref := hpa.Spec.ScaleTargetRef
		key := ref.Kind + "/" + ref.Name
		m[key] = hpa
	}
	return m, nil
}

// aggregatePodMetrics queries the metrics-server for each pod in the list,
// then returns the per-container MAXIMUM CPU and memory observed across all
// pods.  Using the maximum is the conservative choice: it ensures we size for
// the hottest replica.
func (c *Collector) aggregatePodMetrics(
	ctx context.Context,
	namespace string,
	pods []corev1.Pod,
) ([]optimizationv1alpha1.ContainerMetrics, error) {
	// Map containerName -> max{cpu, mem}.
	type agg struct {
		cpuMax int64
		memMax int64
	}
	aggregated := make(map[string]*agg)

	for _, pod := range pods {
		pm, err := c.MetricsClient.MetricsV1beta1().PodMetricses(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			// Pod might have just started; skip quietly.
			continue
		}
		for _, cm := range pm.Containers {
			cpuVal := cm.Usage.Cpu().MilliValue()
			memVal := cm.Usage.Memory().Value()
			if a, ok := aggregated[cm.Name]; ok {
				if cpuVal > a.cpuMax {
					a.cpuMax = cpuVal
				}
				if memVal > a.memMax {
					a.memMax = memVal
				}
			} else {
				aggregated[cm.Name] = &agg{cpuMax: cpuVal, memMax: memVal}
			}
		}
	}

	if len(aggregated) == 0 {
		return nil, nil
	}

	// Produce a stable, sorted result.
	names := make([]string, 0, len(aggregated))
	for n := range aggregated {
		names = append(names, n)
	}
	sort.Strings(names)

	result := make([]optimizationv1alpha1.ContainerMetrics, 0, len(names))
	for _, n := range names {
		a := aggregated[n]
		result = append(result, optimizationv1alpha1.ContainerMetrics{
			Name:          n,
			CPUMilliCores: a.cpuMax,
			MemoryBytes:   a.memMax,
		})
	}
	return result, nil
}

// upsertMetricsHistory creates or updates the MetricsHistory CRD for a
// workload and appends a new data point, pruning old ones.
func (c *Collector) upsertMetricsHistory(
	ctx context.Context,
	namespace, name, kind string,
	dataPoint optimizationv1alpha1.MetricsDataPoint,
	historyWindow time.Duration,
	maxDataPoints int32,
) error {
	mhName := historyName(namespace, name, kind)

	existing := &optimizationv1alpha1.MetricsHistory{}
	err := c.Client.Get(ctx, types.NamespacedName{
		Namespace: c.OperatorNamespace,
		Name:      mhName,
	}, existing)

	cutoff := time.Now().Add(-historyWindow)

	if errors.IsNotFound(err) {
		mh := &optimizationv1alpha1.MetricsHistory{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mhName,
				Namespace: c.OperatorNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "kite",
					"kite.dev/workload-namespace":  namespace,
					"kite.dev/workload-name":       safeLabelValue(name),
					"kite.dev/workload-kind":       strings.ToLower(kind),
				},
			},
			Spec: optimizationv1alpha1.MetricsHistorySpec{
				WorkloadRef: optimizationv1alpha1.WorkloadRef{
					Namespace: namespace,
					Name:      name,
					Kind:      kind,
				},
				MaxDataPoints: maxDataPoints,
				DataPoints:    []optimizationv1alpha1.MetricsDataPoint{dataPoint},
			},
		}
		return c.Client.Create(ctx, mh)
	}
	if err != nil {
		return fmt.Errorf("getting MetricsHistory %s: %w", mhName, err)
	}

	// Append and prune.
	points := existing.Spec.DataPoints
	points = append(points, dataPoint)
	points = pruneDataPoints(points, cutoff, int(maxDataPoints))
	existing.Spec.DataPoints = points
	existing.Spec.MaxDataPoints = maxDataPoints

	return c.Client.Update(ctx, existing)
}

// GetHistory returns the MetricsHistory for a workload, or nil if not found.
func (c *Collector) GetHistory(ctx context.Context, namespace, name, kind string) (*optimizationv1alpha1.MetricsHistory, error) {
	mhName := historyName(namespace, name, kind)
	mh := &optimizationv1alpha1.MetricsHistory{}
	err := c.Client.Get(ctx, types.NamespacedName{
		Namespace: c.OperatorNamespace,
		Name:      mhName,
	}, mh)
	if errors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return mh, nil
}

// resolveNamespaces expands the target selector's Namespaces field.
// When the list is empty all non-system namespaces are returned.
func (c *Collector) resolveNamespaces(ctx context.Context, sel optimizationv1alpha1.TargetSelector) ([]string, error) {
	if len(sel.Namespaces) > 0 {
		result := make([]string, 0, len(sel.Namespaces))
		for _, ns := range sel.Namespaces {
			if !isExcluded(ns, sel.ExcludeNamespaces) {
				result = append(result, ns)
			}
		}
		return result, nil
	}

	// List all namespaces and filter.
	nsList := &corev1.NamespaceList{}
	if err := c.Client.List(ctx, nsList); err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}

	systemPrefixes := []string{"kube-", "cert-manager", "istio-", "monitoring"}
	var result []string
	for _, ns := range nsList.Items {
		if isExcluded(ns.Name, sel.ExcludeNamespaces) {
			continue
		}
		isSystem := false
		for _, p := range systemPrefixes {
			if strings.HasPrefix(ns.Name, p) {
				isSystem = true
				break
			}
		}
		if !isSystem {
			result = append(result, ns.Name)
		}
	}
	return result, nil
}

// --- helpers -----------------------------------------------------------------

func filterRunningPods(pods []corev1.Pod) []corev1.Pod {
	var out []corev1.Pod
	for _, p := range pods {
		if p.Status.Phase == corev1.PodRunning {
			out = append(out, p)
		}
	}
	return out
}

func pruneDataPoints(points []optimizationv1alpha1.MetricsDataPoint, cutoff time.Time, max int) []optimizationv1alpha1.MetricsDataPoint {
	// Remove data points older than the cutoff.
	out := points[:0]
	for _, p := range points {
		if p.Timestamp.Time.After(cutoff) {
			out = append(out, p)
		}
	}
	// Also enforce a hard cap.
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// historyName generates a stable, Kubernetes-safe name for a MetricsHistory
// object.  Names are truncated and hashed when they would exceed 63 characters.
func historyName(namespace, name, kind string) string {
	raw := strings.ToLower(fmt.Sprintf("%s-%s-%s", kind, namespace, name))
	if len(raw) <= 63 {
		return sanitizeName(raw)
	}
	// Use a hash suffix to keep uniqueness while staying within limits.
	h := sha256.Sum256([]byte(raw))
	short := sanitizeName(raw[:50])
	return fmt.Sprintf("%s-%x", short, h[:4])
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func safeLabelValue(s string) string {
	if len(s) > 63 {
		return s[:63]
	}
	return s
}

func isExcluded(ns string, excluded []string) bool {
	for _, e := range excluded {
		if e == ns {
			return true
		}
	}
	return false
}
